package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"kpbot/models"
)

// searchQuery — query-строка категории ноутбуков (порядок: свежие первыми).
// ВАЖНО: строка используется «как есть» и в URL, и в подписи — они должны совпадать.
const searchQuery = "firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort"

// Search — первая страница выдачи: до 30 самых свежих объявлений категории.
func (c *Client) Search(ctx context.Context) ([]models.SearchAd, error) {
	return c.SearchPage(ctx, 1)
}

// SearchPage — страница N выдачи (сортировка по дате публикации, свежие первыми).
func (c *Client) SearchPage(ctx context.Context, page int) ([]models.SearchAd, error) {
	res, err := c.SearchPageFull(ctx, page)
	if err != nil {
		return nil, err
	}
	return res.Ads, nil
}

// SearchPageFull — страница N вместе с метаданными выдачи (total, флаги лимитов).
func (c *Client) SearchPageFull(ctx context.Context, page int) (*models.SearchResults, error) {
	path := apiPrefix + "search?order=posted+desc&page=" + strconv.Itoa(page) + "&" + searchQuery
	resp, err := c.do(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("kp search: чтение тела: %w", err)
	}
	if isChallengeBody(body) {
		recordSharedCooldown(ErrChallenge)
		return nil, ErrChallenge
	}
	if resp.StatusCode != http.StatusOK {
		err := decodeError(resp.StatusCode, string(body))
		recordSharedCooldown(err)
		return nil, err
	}

	var sr models.SearchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("kp search: decode: %w", err)
	}
	return &sr.Results, nil
}
