// Package specs — извлечение характеристик (CPU/RAM/SSD) из текстов объявлений
// KP. Быстрый детерминированный слой: то, что распознаётся regex'ами, дальше
// не нужно гонять через Gemini. Что не распозналось — добирается Gemini
// (текст/фото) или помечается «нужно посмотреть».
package specs

import (
	"regexp"
	"strconv"
	"strings"
)

// Specs — распознанные характеристики лота.
type Specs struct {
	CPU   string // нормализованная модель: «i5-1135G7», «Ryzen 5 4600H», «Ultra 7 155H»; пусто, если не определён
	RAMGB int    // 0 — не определено
	SSDGB int    // 0 — не определено
	GPU   string // дискретная видеокарта: «RTX 3060», «RX 6600M»; пусто, если нет
}

var (
	// Intel: «i5-1135G7», «i7 12850HX», «Core i3-1005G1»
	intelRe = regexp.MustCompile(`(?i)\b(?:core\s+)?(i[3579])[-\s]?(\d{4,5}[a-z]{1,4})\b`)
	// Intel Core Ultra: «Ultra 7 155H», «Ultra 9 285HX», а также каталожный
	// формат магазинов «Core Ultra 9 Processor 290HX Plus» (плюс-варианты и
	// многосуффиксные SKU: 290HX, 285HX, …).
	ultraRe = regexp.MustCompile(`(?i)\b(?:core\s+)?ultra\s?([579])\s?(?:processor\s+)?(\d{3}[a-z]{0,4}(?:\s?plus)?)\b`)
	// AMD: «Ryzen 5 4600H», «Ryzen 7 5800U»
	ryzenRe = regexp.MustCompile(`(?i)\bryzen\s?([3579])\s?(\d{4}[a-z]{0,3})\b`)

	thinkPadGenRe = regexp.MustCompile(`(?i)\b(?:lenovo\s+)?thinkpad\s+([a-z]\d{1,3}s?)\s*(?:gen(?:eration)?|g)\s*([0-9]{1,2})\b`)
	lenovoMTMRe   = regexp.MustCompile(`(?i)\b(2[0-9a-z]{9})\b`)
)

// ExtractCPU возвращает нормализованную модель CPU из произвольного текста.
func ExtractCPU(text string) string {
	if m := intelRe.FindStringSubmatch(text); m != nil {
		return strings.ToLower(m[1]) + "-" + strings.ToUpper(m[2])
	}
	if m := ultraRe.FindStringSubmatch(text); m != nil {
		model := strings.ToUpper(m[2])
		model = strings.ReplaceAll(model, " PLUS", " Plus")
		return "Ultra " + m[1] + " " + model
	}
	if m := ryzenRe.FindStringSubmatch(text); m != nil {
		return "Ryzen " + m[1] + " " + strings.ToUpper(m[2])
	}
	return ""
}

// ExtractLaptopModel возвращает явную модель ноутбука из текста, если она
// написана достаточно конкретно для model→specs поиска. CPU по такой модели
// не угадывается локально: это только hint для L3.4/Gemini.
func ExtractLaptopModel(text string) string {
	if m := thinkPadGenRe.FindStringSubmatch(text); m != nil {
		model := "Lenovo ThinkPad " + strings.ToUpper(m[1]) + " Gen " + m[2]
		if code := lenovoMTM(text); code != "" {
			model += " " + code
		}
		return model
	}
	if code := lenovoMTM(text); code != "" && strings.Contains(strings.ToLower(text), "lenovo") {
		return "Lenovo " + code
	}
	return ""
}

func lenovoMTM(text string) string {
	if m := lenovoMTMRe.FindStringSubmatch(text); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

func ExtractExactModelCode(text string) string {
	return lenovoMTM(text)
}

var (
	// все вхождения «NNGB»/«NNG» с позициями — классифицируются по контексту
	gbRe          = regexp.MustCompile(`(?i)\b(\d{1,4})\s*gb?\b`)
	storageRe     = regexp.MustCompile(`(?i)^(?:\s|/|-|,|")*(?:ssd|nvme|m\.?2|pcie|nand)\b`)
	storageBefore = regexp.MustCompile(`(?i)\b(?:ssd|nvme|m\.?2)\s*$`)
	ramBefore     = regexp.MustCompile(`(?i)\b(?:ram|ddr\d?|memorij[ae]?)\s*$`)
	tbRe          = regexp.MustCompile(`(?i)\b(\d)\s*tb\b`)
)

// ramValues — типичные объёмы оперативки (защита от ложных срабатываний).
var ramValues = map[int]bool{
	2: true, 3: true, 4: true, 6: true, 8: true, 12: true, 16: true,
	24: true, 32: true, 48: true, 64: true, 96: true, 128: true,
}

// ExtractMemory определяет RAM и SSD по вхождениям «NNGB» и ключевым словам.
// Логика: GB, сразу за которым идёт ssd/nvme/m.2 — накопитель; перед которым
// ram/ddr — память; первое «чистое» GB типового объёма — память (в заголовках
// KP порядок почти всегда «CPU/RAM/SSD»).
func ExtractMemory(text string) (ramGB, ssdGB int) {
	for _, m := range gbRe.FindAllStringSubmatchIndex(text, -1) {
		val, _ := strconv.Atoi(text[m[2]:m[3]])
		after := text[m[1]:min(m[1]+12, len(text))]
		before := text[max(0, m[0]-16):m[0]]

		switch {
		case val >= 64 && (storageRe.MatchString(after) || storageBefore.MatchString(before)):
			// 64+ GB рядом с ssd/nvme — накопитель (16gb/SSD — это память + диск без объёма)
			if ssdGB == 0 {
				ssdGB = val
			}
		case val >= 128 && ramGB > 0:
			// KP-заголовки часто пишут компактно: CPU / 16GB / 512GB.
			// После уже найденной RAM крупный объём без контекста — накопитель.
			if ssdGB == 0 {
				ssdGB = val
			}
		case ramBefore.MatchString(before):
			if ramGB == 0 && ramValues[val] {
				ramGB = val
			}
		default:
			if ramGB == 0 && ramValues[val] {
				ramGB = val
			}
		}
	}
	if m := tbRe.FindStringSubmatch(text); m != nil && ssdGB == 0 {
		n, _ := strconv.Atoi(m[1])
		if n >= 1 && n <= 8 {
			ssdGB = n * 1024
		}
	}
	return ramGB, ssdGB
}

var (
	gpuNVRe   = regexp.MustCompile(`(?i)\b(rtx|gtx)\s?-?\s?(\d{4})\s?(ti|super)?\b`)
	gpuRTXARe = regexp.MustCompile(`(?i)\brtx\s?-?\s?a(\d{4})\b`)
	// Ada-поколение рабочих GPU: «RTX 500 Ada», «RTX 2000 Ada», …
	gpuRTXAdaRe = regexp.MustCompile(`(?i)\brtx\s?(\d{3})\s?ada\b`)
	gpuAMDRe    = regexp.MustCompile(`(?i)\brx\s?-?\s?(\d{4})\s?(xtx|xt|m)?\b`)
	gpuMXRe     = regexp.MustCompile(`(?i)\bmx\s?-?\s?(\d{3})\b`)
	gpuQuadroRe = regexp.MustCompile(`(?i)\bquadro\s?([a-z]?\d{3,4}[a-z]?)\b`)
	// T-series без префикса: только ≥1000, чтобы не цеплять ThinkPad T4xx/T5xx.
	gpuTSeriesRe   = regexp.MustCompile(`(?i)\bT(1000|1200|2000)\b`)
	gpuRadeonProRe = regexp.MustCompile(`(?i)\bradeon\s?pro\s?(\d{4}[a-z]?)\b`)
	gpuArcRe       = regexp.MustCompile(`(?i)\barc\s?([a-z]?\d{3}[a-z]?)\b`)
)

// ExtractGPU — дискретная видеокарта из текста: «RTX 3060», «GTX 1650 Ti»,
// «RX 6600M», «MX 450», а также рабочие: «RTX A3000», «Quadro T2000»,
// «T1200», «Radeon Pro 5500M», «Arc A370M». Пусто, если не найдена
// (встройка не считается).
func ExtractGPU(text string) string {
	if m := gpuRTXARe.FindStringSubmatch(text); m != nil {
		return "RTX A" + m[1]
	}
	if m := gpuRTXAdaRe.FindStringSubmatch(text); m != nil {
		return "RTX " + m[1] + " Ada"
	}
	if m := gpuNVRe.FindStringSubmatch(text); m != nil {
		t := strings.ToUpper(m[1]) + " " + m[2]
		if m[3] != "" {
			t += " " + strings.ToUpper(m[3])
		}
		return t
	}
	if m := gpuAMDRe.FindStringSubmatch(text); m != nil {
		t := "RX " + m[1]
		if m[2] != "" {
			t += strings.ToUpper(m[2]) // 6600M остаётся слитно
		}
		return t
	}
	if m := gpuMXRe.FindStringSubmatch(text); m != nil {
		return "MX " + m[1]
	}
	if m := gpuQuadroRe.FindStringSubmatch(text); m != nil {
		return "Quadro " + strings.ToUpper(m[1])
	}
	if m := gpuTSeriesRe.FindStringSubmatch(text); m != nil {
		return "T" + m[1]
	}
	if m := gpuRadeonProRe.FindStringSubmatch(text); m != nil {
		return "Radeon Pro " + m[1]
	}
	if m := gpuArcRe.FindStringSubmatch(text); m != nil {
		return "Arc " + strings.ToUpper(m[1])
	}
	return ""
}

// Extract — полный разбор текста (заголовок + описание).
func Extract(text string) Specs {
	ram, ssd := ExtractMemory(text)
	return Specs{
		CPU:   ExtractCPU(text),
		RAMGB: ram,
		SSDGB: ssd,
		GPU:   ExtractGPU(text),
	}
}
