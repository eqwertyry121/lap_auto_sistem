package filters

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Классы вердиктов.

const (
	ClassShop    = "SHOP"    // L1: магазин/перекуп
	ClassPrivate = "PRIVATE" // L1: частник
	ClassUnknown = "UNKNOWN" // L1: слабые коммерческие маркеры есть, но порога SHOP не хватило

	JunkClean     = "CLEAN"            // L2: чистый лот
	JunkPartsOnly = "PARTS_ONLY"       // L2: труп/запчасти — жёсткий блок
	JunkDefect    = "DEFECT"           // L2: рабочий с дефектом — ⚠️ CHECK
	JunkUncertain = "BROKEN_UNCERTAIN" // L2: маркер хлама сомнителен — ⚠️ CHECK
)

// AdFacts — всё, что фильтры знают о лоте на момент решения.
// MedianEUR = 0 означает «медианы конфигурации нет» (кросс-чек L2 не
// проводится, маркеры решают сами).
type AdFacts struct {
	Title           string
	Description     string // plain text без HTML
	Seller          string
	SellerLabel     string // ручная разметка продавца: SHOP/PRIVATE
	AdLabel         string // ручная разметка объявления: JUNK/CLEAN
	Condition       string // поле KP: new/used/broken/""
	IsTrader        bool   // KP пометил торговца (М1)
	KPIzlog         bool   // витрина KP Izlog (М2)
	IsRenewed       bool   // автообновление объявления
	PriceEUR        float64
	MedianEUR       float64
	SellerAds       int // активных лотов продавца по реестру (М3)
	SellerRecentAds int // лотов этого user_id, недавно виденных в выдаче/research
	SellerAgeDays   int // возраст аккаунта в днях; -1 = неизвестен (М6)
	Reviews         int // отзывы продавца

	// Исторические метки из research.db: если этот user_id уже встречался как
	// торговец/витрина, текущий лот считаем коммерческим даже без свежей метки.
	SellerTraderSeen  bool
	SellerKPIzlogSeen bool
}

// Verdict — результат фильтра: класс + причины (для аудита и дайджеста).
type Verdict struct {
	Class   string
	Reasons []string
}

// ---------- L1: фильтр магазинов ----------

var (
	// М4: пункты прайс-листа «#NN-…» (витрина магазина в одном лоте).
	priceListItemRe = regexp.MustCompile(`#\d{1,3}\s*[-–]`)
	// М4: ячейка «| … €» — цена, стоящая за трубой: признак табличного
	// прайса. Описания хранятся плоским текстом (HTML-теги → пробелы),
	// поэтому «строки» ищем без привязки к переводам строк.
	pipePriceRe          = regexp.MustCompile(`\|[^|]{0,40}€`)
	pluralLaptopTitleRe  = regexp.MustCompile(`\blaptopovi\b`)
	multipleBrandTitleRe = regexp.MustCompile(`\b(acer|asus|dell|hp|lenovo|msi|fujitsu|toshiba|alienware|gigabyte|razer)\b[^,]{0,40},[^,]{0,40}\b(acer|asus|dell|hp|lenovo|msi|fujitsu|toshiba|alienware|gigabyte|razer)\b`)
)

var sellerNameShopMarkers = []string{
	"shop", "store", "laptop centar", "centar laptopa", "laptop servis",
	"servis racunara", "racunari", "kompjuteri", "computer", "doo", "d.o.o",
	"trade", "komerc", "best buy", "tehnika", "komponente", "western europe",
	"pc service", "it servis", "servis-prodaja", "it-zona", "techno-zona",
}

const (
	sellerAdsHardThreshold    = 12
	sellerAdsSoftThreshold    = 6
	sellerRecentHardThreshold = 8
	sellerRecentSoftThreshold = 4
	highReviewsThreshold      = 25
)

// L1 — фильтр магазинов. PLAN_v8 (2026-08-06): к детекции по тексту описания
// (М4, М5) добавлены ЧЕСТНЫЕ метки KP (М1, М2) — решение 2026-08-05 «метки не
// использовать» отменено пользователем после починки is_trader: KP присылает
// объект trader ВСЕМ, и только title «Trgovac» означает заявленного торговца
// (регрессия в models/models_test.go). Поведенческие подсчёты используются
// консервативно: история торговца/витрины режет сразу, много лотов режет
// только на высоком пороге или в связке с автообновлением/отзывами/маркерами.
// UNKNOWN означает не чистого частника, а слабые shop-маркеры ниже порога.
func L1(f AdFacts) Verdict {
	switch manualLabel(f.SellerLabel) {
	case "SHOP":
		return Verdict{ClassShop, []string{"manual label: seller=SHOP"}}
	}

	// М1/М2 — честные метки KP: заявленный торговец или витрина.
	if f.IsTrader {
		return Verdict{ClassShop, []string{"М1: KP пометил продавца торговцем (title «Trgovac»)"}}
	}
	if f.KPIzlog {
		return Verdict{ClassShop, []string{"М2: витрина KP Izlog — профессиональный продавец"}}
	}
	if f.SellerTraderSeen {
		return Verdict{ClassShop, []string{"М1-history: этот user_id уже встречался как Trgovac"}}
	}
	if f.SellerKPIzlogSeen {
		return Verdict{ClassShop, []string{"М2-history: этот user_id уже встречался с KP Izlog"}}
	}
	if manualLabel(f.SellerLabel) == "PRIVATE" {
		return Verdict{ClassPrivate, []string{"manual label: seller=PRIVATE"}}
	}

	textNorm := normalize(f.Title + " " + f.Description)
	titleNorm := normalize(f.Title)
	if pluralLaptopTitleRe.MatchString(titleNorm) {
		return Verdict{ClassShop, []string{"М4: заголовок во множественном числе («laptopovi») — похоже на мультилистинг"}}
	}
	if multipleBrandTitleRe.MatchString(titleNorm) {
		return Verdict{ClassShop, []string{"М4: несколько брендов в заголовке — похоже на мультилистинг"}}
	}
	sellerNorm := normalize(f.Seller)
	for _, m := range sellerNameShopMarkers {
		if strings.Contains(sellerNorm, m) {
			return Verdict{ClassShop, []string{"М3: коммерческое имя продавца («" + m + "»)"}}
		}
	}

	// М4 — мультилистинг: один лот = прайс-лист на много машин.
	if n := len(priceListItemRe.FindAllString(textNorm, -1)); n >= 3 {
		return Verdict{ClassShop,
			[]string{fmt.Sprintf("М4: прайс-лист из %d пунктов в одном лоте", n)}}
	}
	if n := len(pipePriceRe.FindAllString(textNorm, -1)); n >= 4 {
		return Verdict{ClassShop,
			[]string{fmt.Sprintf("М4: %d ячеек «| … €» — табличный прайс", n)}}
	}

	// М5 — маркеры описаний (взвешенная сумма слов из текста; М7 «мультигород»
	// считается внутри markerWeight).
	weight, reasons := markerWeight(textNorm)
	if f.SellerAds >= sellerAdsHardThreshold {
		return Verdict{ClassShop,
			[]string{fmt.Sprintf("М3: у продавца %d лотов в датасете (≥%d) — похоже на перекупа/магазин",
				f.SellerAds, sellerAdsHardThreshold)}}
	}
	if f.SellerRecentAds >= sellerRecentHardThreshold {
		return Verdict{ClassShop,
			[]string{fmt.Sprintf("М3-recent: у продавца %d свежих лотов (≥%d) — активный перекуп/магазин",
				f.SellerRecentAds, sellerRecentHardThreshold)}}
	}
	if f.SellerAds >= sellerAdsSoftThreshold && (f.IsRenewed || f.Reviews >= highReviewsThreshold || weight > 0) {
		why := []string{fmt.Sprintf("М3: у продавца %d лотов в датасете (≥%d)", f.SellerAds, sellerAdsSoftThreshold)}
		if f.IsRenewed {
			why = append(why, "автообновление объявления")
		}
		if f.Reviews >= highReviewsThreshold {
			why = append(why, fmt.Sprintf("%d отзывов", f.Reviews))
		}
		why = append(why, reasons...)
		return Verdict{ClassShop, why}
	}
	if f.SellerRecentAds >= sellerRecentSoftThreshold && (f.IsRenewed || f.Reviews >= highReviewsThreshold || weight > 0) {
		why := []string{fmt.Sprintf("М3-recent: у продавца %d свежих лотов (≥%d)", f.SellerRecentAds, sellerRecentSoftThreshold)}
		if f.IsRenewed {
			why = append(why, "автообновление объявления")
		}
		if f.Reviews >= highReviewsThreshold {
			why = append(why, fmt.Sprintf("%d отзывов", f.Reviews))
		}
		why = append(why, reasons...)
		return Verdict{ClassShop, why}
	}
	if weight >= ThresholdL1 {
		return Verdict{ClassShop,
			append([]string{fmt.Sprintf("М5: вес маркеров %.1f ≥ %.1f", weight, ThresholdL1)}, reasons...)}
	}

	if len(reasons) > 0 {
		// маркеры были, но порога не хватило — запоминаем для аудита
		return Verdict{ClassUnknown, reasons}
	}
	return Verdict{ClassPrivate, nil}
}

// ---------- L2: фильтр хлама ----------

// L2 — фильтр хлама (PLAN_v4 §3.3). Кросс-чек ценой: маркер PARTS_ONLY
// при цене внутри распределения конфигурации (≥60% медианы) считается
// сомнительным → BROKEN_UNCERTAIN вместо тихого блока.
func L2(f AdFacts) Verdict {
	switch manualLabel(f.AdLabel) {
	case "JUNK", "PARTS_ONLY", "BROKEN":
		return Verdict{JunkPartsOnly, []string{"manual label: ad=JUNK"}}
	}
	if f.Condition == conditionBroken {
		return Verdict{JunkPartsOnly, []string{"поле KP condition=broken"}}
	}
	switch manualLabel(f.AdLabel) {
	case "CLEAN":
		return Verdict{JunkClean, []string{"manual label: ad=CLEAN"}}
	case "DEFECT":
		return Verdict{JunkDefect, []string{"manual label: ad=DEFECT"}}
	}

	textNorm := normalize(f.Title + " " + f.Description)

	if m := firstMarker(textNorm, defectMarkers); m != "" {
		if !(m == "bez punjaca" && chargerIsIncluded(textNorm)) {
			return Verdict{JunkDefect, []string{"дефект: «" + m + "»"}}
		}
	}

	if m := firstMarker(textNorm, partsOnlyMarkers); m != "" {
		if f.MedianEUR > 0 && f.PriceEUR >= 0.6*f.MedianEUR {
			return Verdict{JunkUncertain,
				[]string{fmt.Sprintf("маркер «%s», но цена %.0f€ ≥ 60%% медианы %.0f€ — проверить вручную",
					m, f.PriceEUR, f.MedianEUR)}}
		}
		return Verdict{JunkPartsOnly, []string{"маркер «" + m + "»"}}
	}

	for _, m := range defectMarkers {
		if !strings.Contains(textNorm, m) {
			continue
		}
		// «bez punjaca» часто не дефект, а условие: зарядка в комплекте,
		// «без зарядки» — вариант со скидкой (кейс VX15, 2026-08-06).
		if m == "bez punjaca" && chargerIsIncluded(textNorm) {
			continue
		}
		return Verdict{JunkDefect, []string{"дефект: «" + m + "»"}}
	}

	if m := firstMarker(textNorm, uncertainBrokenMarkers); m != "" {
		if f.MedianEUR <= 0 {
			return Verdict{JunkUncertain, []string{"широкий маркер «" + m + "» без ценового кросс-чека"}}
		}
		if f.PriceEUR >= 0.6*f.MedianEUR {
			return Verdict{JunkUncertain,
				[]string{fmt.Sprintf("маркер «%s», но цена %.0f€ ≥ 60%% медианы %.0f€ — проверить вручную",
					m, f.PriceEUR, f.MedianEUR)}}
		}
		return Verdict{JunkPartsOnly, []string{"маркер «" + m + "»"}}
	}

	return Verdict{JunkClean, nil}
}

func manualLabel(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// chargerIsIncluded — «bez punjaca» НЕ дефект, если (а) зарядка явно в
// комплекте («sa punjacem» и пр.) или (б) ВСЕ вхождения стоят в условном
// обороте («ukoliko zelite bez punjaca cena je 200e» — опция скидки).
func firstMarker(textNorm string, markers []string) string {
	ordered := append([]string(nil), markers...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, m := range ordered {
		if strings.Contains(textNorm, m) {
			return m
		}
	}
	return ""
}

func chargerIsIncluded(textNorm string) bool {
	for _, ok := range []string{"sa punjacem", "punjac ide uz", "punjac u kompletu", "punjac je ukljucen"} {
		if strings.Contains(textNorm, ok) {
			return true
		}
	}
	rest := textNorm
	for {
		idx := strings.Index(rest, "bez punjaca")
		if idx < 0 {
			return true
		}
		from := idx - 30
		if from < 0 {
			from = 0
		}
		window := rest[from:idx]
		if !strings.Contains(window, "ukoliko") && !strings.Contains(window, "ako ") {
			return false
		}
		rest = rest[idx+len("bez punjaca"):]
	}
}
