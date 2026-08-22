package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"kpbot/models"
)

// FetchDetail — полные данные объявления: описание (HTML), HD-фото, атрибуты, продавец.
// Вызывается ТОЛЬКО для новых ad_id (экономия лимитов KP).
func (c *Client) FetchDetail(ctx context.Context, adID int64) (*models.AdDetail, error) {
	path := apiPrefix + "eds/" + strconv.FormatInt(adID, 10)
	resp, err := c.do(ctx, path)
	if err != nil {
		if errors.Is(err, ErrChallenge) {
			return c.detailFromWeb(ctx, adID)
		}
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("kp eds: чтение тела: %w", err)
	}
	if isChallengeBody(body) {
		c.recordSharedCooldown(ErrChallenge)
		return c.detailFromWeb(ctx, adID)
	}
	if resp.StatusCode != http.StatusOK {
		err := decodeError(resp.StatusCode, string(body))
		c.recordSharedCooldown(err)
		return nil, err
	}

	var dr models.DetailResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		return nil, fmt.Errorf("kp eds: decode: %w", err)
	}
	if dr.Info == nil {
		return nil, fmt.Errorf("kp eds: пустой info в ответе")
	}
	return dr.Info, nil
}
