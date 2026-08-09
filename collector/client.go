package collector

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"kpbot/models"
)

var (
	ErrRateLimited = errors.New("kp: 429 too many requests")
	ErrNotFound    = errors.New("kp: объявление не найдено")
	ErrChallenge   = errors.New("kp: антибот-челлендж (areYouHuman)")
)

const apiPrefix = "/api/web/v1/"

// kpSalt — «соль» подписи, восстановленная из обфусцированного JS-бандла KP.
// part1: каждый символ "Nvt3ZRK" x5; part2: "f64nGz7"[2:]+"f64nGz7"[:2];
// part3: "GdxpEd5".replace(/(..)./g,"$1|").
var kpSalt = func() string {
	var b strings.Builder
	for _, c := range "Nvt3ZRK" {
		for i := 0; i < 5; i++ {
			b.WriteRune(c)
		}
	}
	b.WriteString("f64nGz7"[2:])
	b.WriteString("f64nGz7"[:2])
	b.WriteString("Gd|pE|5")
	return b.String()
}()

// kpSignature — реплика клиентской generateSignature: SHA1(path + body + salt).
// path — полный путь с query-строкой, напр. "/api/web/v1/search?order=...".
func kpSignature(pathWithQuery string, body []byte) string {
	h := sha1.New()
	h.Write([]byte(pathWithQuery))
	if len(body) > 0 {
		h.Write(body)
	}
	h.Write([]byte(kpSalt))
	return hex.EncodeToString(h.Sum(nil))
}

// Ротация мобильных User-Agent для снижения риска блокировок.
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36",
}

type Client struct {
	http  *http.Client
	uaIdx atomic.Uint64
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}}
}

// do выполняет GET к /api/web/v1/<pathWithQuery> с обязательной подписью.
func (c *Client) do(ctx context.Context, pathWithQuery string) (*http.Response, error) {
	url := models.BaseURL + pathWithQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("accept-language", "sr-RS,sr;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", userAgents[c.uaIdx.Add(1)%uint64(len(userAgents))])
	req.Header.Set("origin", models.BaseURL)
	req.Header.Set("referer", models.BaseURL+"/")
	req.Header.Set("x-kp-channel", "desktop_react")
	req.Header.Set("x-kp-signature", kpSignature(pathWithQuery, nil))
	req.Header.Set("x-kp-webdriver", "false")
	req.Header.Set("x-kp-dark", "false")
	req.Header.Set("x-kp-theme", "system")
	return c.http.Do(req)
}

// decodeError разбирает тело ошибки KP вида {"success":false,"errors":[...]}.
func decodeError(status int, body string) error {
	switch status {
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusNotFound, http.StatusGone:
		return ErrNotFound
	case http.StatusBadRequest:
		if strings.Contains(body, "areYouHuman") {
			return ErrChallenge
		}
	}
	return fmt.Errorf("kp: HTTP %d: %s", status, truncate(body, 200))
}

func isChallengeBody(body []byte) bool {
	s := string(body)
	return strings.Contains(s, `"captchaSiteKey"`) || strings.Contains(s, `"areYouHuman"`)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
