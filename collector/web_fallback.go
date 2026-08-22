package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"kpbot/models"
)

const laptopPagePath = "/kompjuteri-laptop-i-tablet/laptopovi/grupa/1221/101/"

type nextDataEnvelope struct {
	Props struct {
		InitialReduxState nextReduxState `json:"initialReduxState"`
	} `json:"props"`
}

type nextReduxState struct {
	Ad           nextAdState      `json:"ad"`
	AdNavigation nextAdNavigation `json:"adNavigation"`
}

type nextAdState struct {
	ByID map[string]nextAd `json:"byId"`
}

type nextAdNavigation struct {
	AdsIDs []int64           `json:"adsIds"`
	ByID   map[string]nextAd `json:"byId"`
	Page   int               `json:"page"`
	Pages  int               `json:"pages"`
	Total  int               `json:"total"`
}

type nextAd struct {
	ID                        int64              `json:"id"`
	UserID                    int64              `json:"userId"`
	Name                      string             `json:"name"`
	Description               string             `json:"description"`
	DescriptionSnippetDecoded string             `json:"descriptionSnippetDecoded"`
	PriceNumber               float64            `json:"priceNumber"`
	CurrencyAcronym           string             `json:"currencyAcronym"`
	Location                  string             `json:"location"`
	AdURL                     string             `json:"adUrl"`
	PostedRaw                 string             `json:"postedRaw"`
	ConditionID               string             `json:"conditionId"`
	IsExchange                bool               `json:"isExchange"`
	KPIzlog                   bool               `json:"kpizlog"`
	IsRenewed                 bool               `json:"isRenewed"`
	ViewCount                 string             `json:"viewCount"`
	SmallImage                string             `json:"smallImage"`
	OwnerName                 string             `json:"ownerName"`
	Photos                    []nextPhoto        `json:"photos"`
	AdAttributes              []models.Attribute `json:"adAttributes"`
	User                      nextUser           `json:"user"`
}

type nextPhoto struct {
	Thumbnail  string `json:"thumbnail"`
	Original   string `json:"original"`
	Fullscreen string `json:"fullscreen"`
}

type nextUser struct {
	Username        string `json:"username"`
	Created         string `json:"created"`
	ReviewsPositive string `json:"reviewsPositive"`
	ReviewsNegative string `json:"reviewsNegative"`
	Trader          *struct {
		Title string `json:"title"`
	} `json:"trader"`
}

func (c *Client) searchPageFromWeb(ctx context.Context, page int) (*models.SearchResults, error) {
	pageURL := models.BaseURL + laptopPagePath + strconv.Itoa(page) + "?order=posted%20desc"
	var state nextReduxState
	if err := c.fetchNextState(ctx, pageURL, &state); err != nil {
		return nil, fmt.Errorf("kp web search: %w", err)
	}
	nav := state.AdNavigation
	result := &models.SearchResults{Total: nav.Total, Pages: nav.Pages, Page: nav.Page}
	result.Ads = make([]models.SearchAd, 0, len(nav.AdsIDs))
	for _, id := range nav.AdsIDs {
		a, ok := nav.ByID[strconv.FormatInt(id, 10)]
		if !ok {
			continue
		}
		result.Ads = append(result.Ads, models.SearchAd{
			AdID: id, UserID: a.UserID, Name: a.Name, Price: models.FlexFloat(a.PriceNumber),
			Currency: a.CurrencyAcronym, LocationName: a.Location, AdURL: a.AdURL,
			Posted: a.PostedRaw, PhotoPath1: a.SmallImage, Condition: a.ConditionID,
			Exchange: a.IsExchange, KPIzlog: a.KPIzlog, IsRenewed: a.IsRenewed,
			ViewCount: int64(models.ParsePrice(a.ViewCount)), DescriptionSnip: a.DescriptionSnippetDecoded,
		})
	}
	if len(result.Ads) == 0 {
		return nil, fmt.Errorf("пустая серверная выдача")
	}
	return result, nil
}

func (c *Client) detailFromWeb(ctx context.Context, adID int64) (*models.AdDetail, error) {
	pageURL := fmt.Sprintf("%s/kompjuteri-laptop-i-tablet/laptopovi/x/oglas/%d", models.BaseURL, adID)
	var state nextReduxState
	if err := c.fetchNextState(ctx, pageURL, &state); err != nil {
		return nil, fmt.Errorf("kp web detail: %w", err)
	}
	a, ok := state.Ad.ByID[strconv.FormatInt(adID, 10)]
	if !ok {
		return nil, ErrNotFound
	}
	d := &models.AdDetail{
		AdID: a.ID, Name: a.Name, Description: a.Description,
		Price: models.FlexFloat(a.PriceNumber), Currency: a.CurrencyAcronym,
		Owner: a.OwnerName, AdURL: a.AdURL, Condition: a.ConditionID,
		KPIzlog: a.KPIzlog, Attributes: a.AdAttributes,
	}
	d.User.Name = a.User.Username
	d.User.Created = a.User.Created
	d.User.Reviews = models.FlexInt(parseCount(a.User.ReviewsPositive) + parseCount(a.User.ReviewsNegative))
	if a.User.Trader != nil {
		d.User.Trader = &struct {
			Title string `json:"title"`
		}{Title: a.User.Trader.Title}
	}
	for _, p := range a.Photos {
		big := p.Fullscreen
		if big == "" {
			big = p.Original
		}
		d.Photos = append(d.Photos, models.PhotoDoc{Path: p.Thumbnail, Big: big})
	}
	return d, nil
}

func (c *Client) fetchNextState(ctx context.Context, pageURL string, state *nextReduxState) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("accept", "text/html,application/xhtml+xml")
	req.Header.Set("accept-language", "sr-RS,sr;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("user-agent", userAgents[c.uaIdx.Add(1)%uint64(len(userAgents))])
	resp, err := c.http.Do(req)
	if err != nil {
		return sanitizedRequestError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return err
	}
	raw, err := extractNextData(body)
	if err != nil {
		return err
	}
	var envelope nextDataEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode __NEXT_DATA__: %w", err)
	}
	*state = envelope.Props.InitialReduxState
	return nil
}

func extractNextData(body []byte) ([]byte, error) {
	marker := []byte(`id="__NEXT_DATA__"`)
	i := bytes.Index(body, marker)
	if i < 0 {
		return nil, fmt.Errorf("__NEXT_DATA__ не найден")
	}
	startRel := bytes.IndexByte(body[i:], '>')
	if startRel < 0 {
		return nil, fmt.Errorf("повреждён тег __NEXT_DATA__")
	}
	start := i + startRel + 1
	endRel := bytes.Index(body[start:], []byte("</script>"))
	if endRel < 0 {
		return nil, fmt.Errorf("__NEXT_DATA__ не закрыт")
	}
	return []byte(html.UnescapeString(string(body[start : start+endRel]))), nil
}

func parseCount(s string) int64 {
	s = strings.NewReplacer(".", "", ",", "", " ", "").Replace(s)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func sanitizedRequestError(err error) error {
	if uerr, ok := err.(*url.Error); ok {
		return uerr.Err
	}
	return err
}
