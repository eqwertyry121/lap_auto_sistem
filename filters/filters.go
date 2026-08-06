package filters

import (
	"fmt"
	"regexp"
	"strings"
)

// Классы вердиктов.

const (
	ClassShop    = "SHOP"    // L1: магазин/перекуп
	ClassPrivate = "PRIVATE" // L1: частник
	ClassUnknown = "UNKNOWN" // L1: данных мало; трактуется как PRIVATE (принцип 7)

	JunkClean     = "CLEAN"            // L2: чистый лот
	JunkPartsOnly = "PARTS_ONLY"       // L2: труп/запчасти — жёсткий блок
	JunkDefect    = "DEFECT"           // L2: рабочий с дефектом — ⚠️ CHECK
	JunkUncertain = "BROKEN_UNCERTAIN" // L2: маркер хлама сомнителен — ⚠️ CHECK
)

// AdFacts — всё, что фильтры знают о лоте на момент решения.
// MedianEUR = 0 означает «медианы конфигурации нет» (кросс-чек L2 не
// проводится, маркеры решают сами).
type AdFacts struct {
	Title         string
	Description   string // plain text без HTML
	Seller        string
	Condition     string // поле KP: new/used/broken/""
	IsTrader      bool   // KP пометил торговца (М1)
	KPIzlog       bool   // витрина KP Izlog (М2)
	PriceEUR      float64
	MedianEUR     float64
	SellerAds     int // активных лотов продавца по реестру (М3)
	SellerAgeDays int // возраст аккаунта в днях; -1 = неизвестен (М6)
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
	pipePriceRe = regexp.MustCompile(`\|[^|]{0,40}€`)
)

// L1 — фильтр магазинов. Решение 2026-08-05: детекция ТОЛЬКО по тексту
// описания. Метки интерфейса KP («Trgovac»=IsTrader, «KP Izlog»=KPIzlog)
// и поведенческие подсчёты (число лотов, возраст аккаунта) НЕ используются —
// только слова и структура самого описания. UNKNOWN трактуется вызывающим
// кодом как PRIVATE.
func L1(f AdFacts) Verdict {
	textNorm := normalize(f.Title + " " + f.Description)

	// М4 — мультилистинг: один лот = прайс-лист на много машин.
	if n := len(priceListItemRe.FindAllString(textNorm, -1)); n >= 3 {
		return Verdict{ClassShop,
			[]string{fmt.Sprintf("М4: прайс-лист из %d пунктов в одном лоте", n)}}
	}
	if n := len(pipePriceRe.FindAllString(textNorm, -1)); n >= 4 {
		return Verdict{ClassShop,
			[]string{fmt.Sprintf("М4: %d ячеек «| … €» — табличный прайс", n)}}
	}

	// М5 — маркеры описаний (взвешенная сумма слов из текста).
	weight, reasons := markerWeight(textNorm)
	if weight >= ThresholdL1 {
		return Verdict{ClassShop,
			append([]string{fmt.Sprintf("М5: вес маркеров %.1f ≥ %.1f", weight, ThresholdL1)}, reasons...)}
	}

	if len(reasons) > 0 {
		// маркеры были, но порога не хватило — запоминаем для аудита
		return Verdict{ClassUnknown, reasons}
	}
	return Verdict{ClassUnknown, nil}
}

// ---------- L2: фильтр хлама ----------

// L2 — фильтр хлама (PLAN_v4 §3.3). Кросс-чек ценой: маркер PARTS_ONLY
// при цене внутри распределения конфигурации (≥60% медианы) считается
// сомнительным → BROKEN_UNCERTAIN вместо тихого блока.
func L2(f AdFacts) Verdict {
	if f.Condition == conditionBroken {
		return Verdict{JunkPartsOnly, []string{"поле KP condition=broken"}}
	}

	textNorm := normalize(f.Title + " " + f.Description)

	for _, m := range partsOnlyMarkers {
		if strings.Contains(textNorm, m) {
			if f.MedianEUR > 0 && f.PriceEUR >= 0.6*f.MedianEUR {
				return Verdict{JunkUncertain,
					[]string{fmt.Sprintf("маркер «%s», но цена %.0f€ ≥ 60%% медианы %.0f€ — проверить вручную",
						m, f.PriceEUR, f.MedianEUR)}}
			}
			return Verdict{JunkPartsOnly, []string{"маркер «" + m + "»"}}
		}
	}

	for _, m := range defectMarkers {
		if strings.Contains(textNorm, m) {
			return Verdict{JunkDefect, []string{"дефект: «" + m + "»"}}
		}
	}

	return Verdict{JunkClean, nil}
}
