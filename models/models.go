package models

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// BaseURL — корень сайта KP. Все относительные пути из API домножаются на него.
const BaseURL = "https://www.kupujemprodajem.com"

// ---------- Статусы обработки лота ----------

type Status string

type ProcessState string

const (
	ProcessDiscovered    ProcessState = "DISCOVERED"
	ProcessDetailPending ProcessState = "DETAIL_PENDING"
	ProcessEnrichPending ProcessState = "ENRICH_PENDING"
	ProcessEvaluating    ProcessState = "EVALUATING"
	ProcessAlertPending  ProcessState = "ALERT_PENDING"
	ProcessDone          ProcessState = "DONE"
	ProcessDead          ProcessState = "DEAD"
)

const (
	StatusNew         Status = "NEW"          // новый лот, ждёт обработки
	StatusScanned     Status = "SCANNED"      // добавлен сканером рынка (исторические данные)
	StatusSkippedSpam Status = "SKIPPED_SPAM" // магазин/партия товара
	StatusSkippedBan  Status = "SKIPPED_BAN"  // запрещённая линейка (MacBook, PLAN_v5)
	StatusAlerted     Status = "ALERTED"      // отправлен ALERT в Telegram
	StatusNeedCheck   Status = "NEED_CHECK"   // отправлен NEED CHECK
	StatusNoDeal      Status = "NO_DEAL"      // проанализирован, выгоды нет
	StatusError       Status = "ERROR"        // ошибка анализа
)

// ---------- Вердикт Gemini ----------

type Verdict struct {
	IsDeal          bool    `json:"is_deal"`
	NeedCheck       bool    `json:"need_check"`
	EstimatedProfit float64 `json:"estimated_profit"`
	Reason          string  `json:"reason"`
	Specs           string  `json:"specs"`
	ModelFound      bool    `json:"model_found"`
}

// ---------- Запись market_listings ----------

type Listing struct {
	AdID           int64
	Title          string
	Price          float64
	Currency       string
	URL            string
	Description    string
	Seller         string
	Status         Status
	ProcessState   ProcessState
	AttemptCount   int
	NextAttemptAt  time.Time
	LeaseUntil     time.Time
	LastError      string
	Verdict        Verdict
	SyncedToSheets bool
	CreatedAt      time.Time
}

// ---------- KP Search API ----------
// Реальный ответ: { "success":true, "results": { "total":N, "pages":N, "ads":[...] } }

type SearchResponse struct {
	Success bool          `json:"success"`
	Results SearchResults `json:"results"`
}

type SearchResults struct {
	Total           int        `json:"total"`
	Pages           int        `json:"pages"`
	Page            int        `json:"page"`
	HasReachedMax   bool       `json:"hasReachedMax"`
	HasReachedLimit bool       `json:"hasReachedLimit"`
	Ads             []SearchAd `json:"ads"`
}

type SearchAd struct {
	AdID         int64     `json:"ad_id"`
	Name         string    `json:"name"`
	Price        FlexFloat `json:"price"`
	Currency     string    `json:"currency"`
	LocationName string    `json:"location_name"`
	AdURL        string    `json:"ad_url"` // относительный путь
	Posted       string    `json:"posted"`
	UserID       int64     `json:"user_id"`
	PhotoPath1   string    `json:"photo_path1"`

	// Бесплатные сигналы уровня поиска (PLAN_v4, Решение ②): KP отдаёт их
	// в search-ответе — половина L1/L2 доступна без запроса /eds/.
	Condition       string `json:"condition"`           // "new" / "used"
	Exchange        bool   `json:"exchange"`            // продавец рассматривает обмен (zamena)
	KPIzlog         bool   `json:"kpizlog"`             // витрина KP Izlog
	IsRenewed       bool   `json:"is_renewed"`          // автообновление (поведение магазинов)
	ViewCount       int64  `json:"view_count"`          // просмотры (сигнал ликвидности)
	DescriptionSnip string `json:"description_snippet"` // фрагмент описания
}

// FlexFloat — число, которое KP может отдать и как JSON-число, и как строку.
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(data []byte) error {
	var v float64
	if err := json.Unmarshal(data, &v); err == nil {
		*f = FlexFloat(v)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexFloat(ParsePrice(s))
		return nil
	}
	*f = 0
	return nil
}

// FlexInt — целое, которое KP может отдать и как JSON-число, и как строку
// (user.reviews на живом API приходит в обоих видах).
type FlexInt int64

func (f *FlexInt) UnmarshalJSON(data []byte) error {
	var v int64
	if err := json.Unmarshal(data, &v); err == nil {
		*f = FlexInt(v)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		*f = FlexInt(v)
		return nil
	}
	*f = 0
	return nil
}

// URL — абсолютная ссылка на объявление.
func (a *SearchAd) URL() string { return AbsURL(a.AdURL) }

// ---------- KP Ad Detail API (/eds/{id}) ----------
// Реальный ответ: { "success":true, "info": { ... } }

type DetailResponse struct {
	Success bool      `json:"success"`
	Info    *AdDetail `json:"info"`
}

type AdDetail struct {
	AdID        int64       `json:"ad_id"`
	Name        string      `json:"name"`
	Description string      `json:"description"` // HTML
	Price       FlexFloat   `json:"price"`
	Currency    string      `json:"currency"`
	Owner       string      `json:"owner"`
	AdURL       string      `json:"ad_url"`    // относительный путь
	Condition   string      `json:"condition"` // "new" / "used" — поле самого KP
	KPIzlog     bool        `json:"kpizlog"`   // витрина продавца (маркер магазина)
	Photos      []PhotoDoc  `json:"photos"`
	Attributes  []Attribute `json:"ad_attributes"`
	User        struct {
		Name   string `json:"name"`
		Trader *struct {
			Title string `json:"title"` // «Trgovac» или «Nije trgovac» — объект приходит ВСЕМ
		} `json:"trader"`
		Reviews FlexInt `json:"reviews"` // число отзывов (KP может отдать и строкой)
		Created string  `json:"created"` // регистрация аккаунта «2006-01-02 15:04:05»
	} `json:"user"`
}

// IsTrader — KP явно пометил продавца как торговца (магазин).
// ВАЖНО: объект trader KP присылает ВСЕМ продавцам — частникам с
// title «Nije trgovac». Проверка присутствия поля (Trader != nil) давала
// 100% ложных срабатываний (аудит 2026-08-06: is_trader=1 у всех лотов с
// деталями, пул медиан лишился всего детального слоя). Решает только title.
func (d *AdDetail) IsTrader() bool {
	return d.User.Trader != nil &&
		strings.EqualFold(strings.TrimSpace(d.User.Trader.Title), "Trgovac")
}

type PhotoDoc struct {
	Path string `json:"path"`
	Big  string `json:"big"` // увеличенная версия — её и шлём в Vision
}

// BestURL — абсолютный путь к наибольшей доступной версии фото.
func (p *PhotoDoc) BestURL() string {
	if p.Big != "" {
		return AbsURL(p.Big)
	}
	return AbsURL(p.Path)
}

type Attribute struct {
	Name  string
	Value string
}

// UnmarshalJSON терпимо разбирает атрибут: значение может быть строкой,
// числом, булом или объектом.
func (a *Attribute) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// возможно, это плоская структура {name, value}
		var flat struct {
			Name  json.RawMessage `json:"name"`
			Value json.RawMessage `json:"value"`
		}
		if err2 := json.Unmarshal(data, &flat); err2 != nil {
			return err
		}
		a.Name = flexString(flat.Name)
		a.Value = flexString(flat.Value)
		return nil
	}
	// типичные ключи в произвольном объекте
	for _, k := range []string{"name", "attribute_name", "label"} {
		if v, ok := raw[k]; ok {
			a.Name = flexString(v)
			break
		}
	}
	for _, k := range []string{"value", "attribute_value", "val"} {
		if v, ok := raw[k]; ok {
			a.Value = flexString(v)
			break
		}
	}
	return nil
}

// Seller — имя продавца (владелец объявления).
func (d *AdDetail) Seller() string {
	if d.Owner != "" {
		return d.Owner
	}
	return d.User.Name
}

// URL — абсолютная ссылка на объявление.
func (d *AdDetail) URL() string { return AbsURL(d.AdURL) }

// ---------- Вспомогательные разборщики ----------

// AbsURL превращает относительный путь KP в абсолютный URL.
func AbsURL(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	if strings.HasPrefix(p, "//") {
		return "https:" + p
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return BaseURL + p
}

// ParsePrice превращает «2 500», «1.299,00», «€220» и т.п. в число.
func ParsePrice(s string) float64 {
	clean := make([]rune, 0, len(s))
	var seps []int
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			clean = append(clean, r)
		case r == '.' || r == ',':
			seps = append(seps, len(clean))
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return 0
	}

	decimalAt := -1
	if len(seps) > 0 {
		last := seps[len(seps)-1]
		digitsAfter := 0
		for i := last + 1; i < len(clean); i++ {
			if clean[i] >= '0' && clean[i] <= '9' {
				digitsAfter++
			}
		}
		digitsBefore := 0
		for i := 0; i < last; i++ {
			if clean[i] >= '0' && clean[i] <= '9' {
				digitsBefore++
			}
		}
		hasDot, hasComma := false, false
		for _, r := range clean {
			hasDot = hasDot || r == '.'
			hasComma = hasComma || r == ','
		}
		switch {
		case hasDot && hasComma:
			if digitsAfter > 0 && digitsAfter <= 2 {
				decimalAt = last
			}
		case len(seps) == 1:
			if digitsAfter > 0 && digitsAfter <= 2 {
				decimalAt = last
			} else if digitsAfter == 3 && digitsBefore == 0 {
				decimalAt = last
			}
		}
	}

	var b strings.Builder
	for i, r := range clean {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case i == decimalAt:
			b.WriteByte('.')
		}
	}
	f, _ := strconv.ParseFloat(b.String(), 64)
	return f
}

// flexString достаёт строку из произвольного JSON-значения.
func flexString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b)
	}
	return strings.TrimSpace(string(raw))
}

// ---------- Форматирование ----------

func NormalizeCurrency(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	if c == "" {
		return "EUR"
	}
	return c
}

func FormatPrice(price float64, currency string) string {
	p := strconv.FormatFloat(math.Round(price), 'f', 0, 64)
	switch NormalizeCurrency(currency) {
	case "EUR":
		return "€" + p
	case "RSD", "DIN":
		return p + " RSD"
	case "BAM", "KM":
		return p + " KM"
	default:
		return p + " " + strings.ToUpper(currency)
	}
}
