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
	// Intel: «i5-1135G7», «i7 12850HX», «Core i3-1005G1»,
	// старые мобильные «i3-330M», Y-series «i5-7Y57». KP часто вставляет
	// пробелы вокруг дефиса: «i5- 10210U», «i7 - 4702MQ».
	intelRe = regexp.MustCompile(`(?i)\b(?:core\s+)?(i[3579])(?:\s*-\s*|\s+)?(\d{3}[a-z]{1,3}|\d{4,5}[a-z]{1,4}|\d[y]\d{2}[a-z]?)\b`)
	// Intel Core Ultra: «Ultra 7 155H», «Ultra 9 285HX», а также каталожный
	// формат магазинов «Core Ultra 9 Processor 290HX Plus» (плюс-варианты и
	// многосуффиксные SKU: 290HX, 285HX, …).
	ultraRe = regexp.MustCompile(`(?i)\b(?:core\s+)?ultra\s?([579])\s?(?:processor\s+)?(\d{3}[a-z]{0,4}(?:\s?plus)?)\b`)
	// Intel Core Ultra shorthand from KP titles: «U7-255U», «U9 275HX», «CU7-356H».
	// Require a letter suffix to avoid old Core 2 Duo names like «U9400».
	ultraShortRe = regexp.MustCompile(`(?i)\bc?u([579])[-\s]?(\d{3}[a-z]{1,4}(?:\s?plus)?)\b`)
	// AMD: «Ryzen 5 4600H», «Ryzen 7 PRO 5850U».
	ryzenRe = regexp.MustCompile(`(?i)\bryzen\s?([3579])\s?(pro\s?)?(\d{4}[a-z]{0,3})\b`)
	// AMD shorthand from KP titles: «R7-7840HS», «R7 PRO 8845HS».
	ryzenShortRe = regexp.MustCompile(`(?i)\br([3579])[-\s]?(pro[-\s]?)?(\d{4}[a-z]{0,3})\b`)
	// AMD Ryzen AI: «Ryzen AI 7 PRO 350», «Ryzen AI 9 HX 370», «R7 AI 350 PRO».
	ryzenAIRe      = regexp.MustCompile(`(?i)\bryzen\s+ai\s+([579])\s+(?:(hx)\s+)?(?:(pro)\s+)?(\d{3})\b`)
	ryzenAIShortRe = regexp.MustCompile(`(?i)\br([579])\s+ai\s+(\d{3})\s*(pro)?\b`)

	thinkPadGenRe = regexp.MustCompile(`(?i)\b(?:lenovo\s+)?thinkpad\s+([a-z]\d{1,3}s?)\s*(?:gen(?:eration)?|g)\s*([0-9]{1,2})\b`)
	lenovoMTMRe   = regexp.MustCompile(`(?i)\b(2[0-9a-z]{9})\b`)
)

// ExtractCPU возвращает нормализованную модель CPU из произвольного текста.
func ExtractCPU(text string) string {
	if m := intelRe.FindStringSubmatch(text); m != nil {
		return strings.ToLower(m[1]) + "-" + strings.ToUpper(m[2])
	}
	if m := ultraRe.FindStringSubmatch(text); m != nil {
		return formatUltraCPU(m[1], m[2])
	}
	if m := ultraShortRe.FindStringSubmatch(text); m != nil {
		return formatUltraCPU(m[1], m[2])
	}
	if m := ryzenAIRe.FindStringSubmatch(text); m != nil {
		return formatRyzenAICPU(m[1], m[2], m[3], m[4])
	}
	if m := ryzenAIShortRe.FindStringSubmatch(text); m != nil {
		return formatRyzenAICPU(m[1], "", m[3], m[2])
	}
	if m := ryzenRe.FindStringSubmatch(text); m != nil {
		return formatRyzenCPU(m[1], m[2], m[3])
	}
	if m := ryzenShortRe.FindStringSubmatch(text); m != nil {
		return formatRyzenCPU(m[1], m[2], m[3])
	}
	return ""
}

func formatUltraCPU(class, model string) string {
	model = strings.ToUpper(model)
	model = strings.ReplaceAll(model, " PLUS", " Plus")
	return "Ultra " + class + " " + model
}

func formatRyzenCPU(class, pro, model string) string {
	out := "Ryzen " + class + " "
	if strings.TrimSpace(pro) != "" {
		out += "PRO "
	}
	return out + strings.ToUpper(model)
}

func formatRyzenAICPU(class, hx, pro, model string) string {
	out := "Ryzen AI " + class + " "
	if strings.TrimSpace(hx) != "" {
		out += "HX "
	}
	if strings.TrimSpace(pro) != "" {
		out += "PRO "
	}
	return out + strings.ToUpper(model)
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
	bareStorageRe = regexp.MustCompile(`(?i)(?:^|[\s/,+-])(\d{3,4})(?:\s*(?:ssd|nvme|g\b|gb\b))?\b`)
)

// ramValues — типичные объёмы оперативки (защита от ложных срабатываний).
var ramValues = map[int]bool{
	2: true, 3: true, 4: true, 6: true, 8: true, 12: true, 16: true,
	24: true, 32: true, 48: true, 64: true, 96: true, 128: true,
}

var storageValues = map[int]bool{
	120: true, 128: true, 180: true, 240: true, 250: true, 256: true,
	480: true, 500: true, 512: true, 960: true, 1000: true, 1024: true,
	2000: true, 2048: true, 4000: true, 4096: true,
}

var gpuVRAMBeforeRe = regexp.MustCompile(`(?i)(?:nvidia\s*)?(?:geforce\s*)?(?:rtx\s*a?\d{3,4}(?:\s*(?:ti|super|ada))?|gtx\s*\d{3,4}(?:\s*ti)?|mx\s*\d{3}|quadro\s*[a-z]?\d{3,4}[a-z]?|[akp]\d{4}[a-z]?|a[1-5]000|t(?:550|1000|1200|2000)|radeon\s*pro\s*\d{4}[a-z]?|rx\s*\d{4}m?|nvidia)\s*[-:]?\s*$`)

// ExtractMemory определяет RAM и SSD по вхождениям «NNGB» и ключевым словам.
// Логика: GB, сразу за которым идёт ssd/nvme/m.2 — накопитель; перед которым
// ram/ddr — память; первое «чистое» GB типового объёма — память (в заголовках
// KP порядок почти всегда «CPU/RAM/SSD»).
func ExtractMemory(text string) (ramGB, ssdGB int) {
	ramEnd := -1
	for _, m := range gbRe.FindAllStringSubmatchIndex(text, -1) {
		val, _ := strconv.Atoi(text[m[2]:m[3]])
		after := text[m[1]:min(m[1]+12, len(text))]
		before := text[max(0, m[0]-16):m[0]]
		if looksLikeGPUVRAM(text, m[0]) {
			continue
		}

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
				ramEnd = m[1]
			}
		default:
			if ramGB == 0 && ramValues[val] {
				ramGB = val
				ramEnd = m[1]
			}
		}
	}
	if m := tbRe.FindStringSubmatch(text); m != nil && ssdGB == 0 {
		n, _ := strconv.Atoi(m[1])
		if n >= 1 && n <= 8 {
			ssdGB = n * 1024
		}
	}
	if ssdGB == 0 && ramEnd >= 0 && ramEnd < len(text) {
		if ssd := extractBareStorageAfterRAM(text[ramEnd:]); ssd > 0 {
			ssdGB = ssd
		}
	}
	return ramGB, ssdGB
}

func extractBareStorageAfterRAM(tail string) int {
	for _, m := range bareStorageRe.FindAllStringSubmatch(tail, -1) {
		val, _ := strconv.Atoi(m[1])
		if storageValues[val] {
			return val
		}
	}
	return 0
}

func looksLikeGPUVRAM(text string, gbStart int) bool {
	if gbStart < 0 || gbStart > len(text) {
		return false
	}
	before := text[max(0, gbStart-48):gbStart]
	return gpuVRAMBeforeRe.MatchString(before)
}

var (
	gpuNVRe     = regexp.MustCompile(`(?i)\b(rtx|gtx)\s?-?\s?(\d{4})\s?(ti|super)?\b`)
	gpuRTXARe   = regexp.MustCompile(`(?i)\brtx\s?-?\s?a(\d{4})\b`)
	gpuRTXProRe = regexp.MustCompile(`(?i)\brtx\s?pro\s?(\d{3,4})\b`)
	// Ada-поколение рабочих GPU: «RTX 500 Ada», «RTX 2000 Ada», …
	gpuRTXAdaRe   = regexp.MustCompile(`(?i)\brtx\s?(\d{3})\s?ada\b`)
	gpuAMDRe      = regexp.MustCompile(`(?i)\brx\s?-?\s?(\d{4})\s?(xtx|xt|m)?\b`)
	gpuMXRe       = regexp.MustCompile(`(?i)\bmx\s?-?\s?(\d{3})\b`)
	gpuQuadroRe   = regexp.MustCompile(`(?i)\bquadro\s?([a-z]?\d{3,4}[a-z]?)\b`)
	gpuBareRTXARe = regexp.MustCompile(`(?i)\ba([1-5]000)\b`)
	gpuBareKRe    = regexp.MustCompile(`(?i)\bk(610|620|1000|1100|2000|2100|3000|3100|4000|4100|5000|5100)m?\b`)
	gpuBareMRe    = regexp.MustCompile(`(?i)\bm(500|520|600|620|1000|1200|2000|2200|3000|4000|5000)m?\b`)
	gpuBarePRe    = regexp.MustCompile(`(?i)\bp(500|600|620|1000|2000|3000|3200|4000|5000)\b`)
	// T-series без префикса: только ≥1000, чтобы не цеплять ThinkPad T4xx/T5xx.
	gpuTSeriesRe   = regexp.MustCompile(`(?i)\bT(1000|1200|2000)\b`)
	gpuRadeonProRe = regexp.MustCompile(`(?i)\bradeon\s?pro\s?(\d{4}[a-z]?)\b`)
	// Intel Arc 130/140/140V in Core Ultra laptops is integrated graphics.
	// Discrete laptop Arc cards are A-series SKUs like Arc A370M.
	gpuArcRe = regexp.MustCompile(`(?i)\barc\s?-?\s?(a\s?\d{3}m?)\b`)

	integratedGPUTextRe = regexp.MustCompile(`(?i)\b(?:integrated|integrisana|integrisani|integrisano|onboard|intel\s*(?:hd|uhd|iris(?:\s*xe)?|arc)(?:\s*(?:graphics|grafika|[0-9]{3,4}[a-z]?))?|intelhd|inteluhd|iris\s*xe|irisxe|uhd\s*(?:graphics|grafika|[0-9]{3,4})|arc\s*(?:130|140)[a-z]?|radeon\s*(?:graphics|grafika|610m|660m|680m|740m|760m|780m|860m|880m|890m))\b`)
	discreteGPUHintRe   = regexp.MustCompile(`(?i)\b(?:nvidia|geforce|rtx|gtx|quadro|radeon\s+pro|rx\s?-?\s?\d{3,4}m?|mx\s?-?\s?\d{3}|dgpu|discrete|dedicated|diskretna|dedicirana|grafi[čc]ka\s+\d+\s*gb|a[1-5]000|t(?:550|1000|1200|2000)|[kmp](?:500|520|600|620|1000|1100|1200|2000|2100|2200|3000|3100|3200|4000|4100|5000|5100)m?)\b`)
)

// ExtractGPU — дискретная видеокарта из текста: «RTX 3060», «GTX 1650 Ti»,
// «RX 6600M», «MX 450», а также рабочие: «RTX A3000», «Quadro T2000»,
// «T1200», «Radeon Pro 5500M», «Arc A370M». Пусто, если не найдена
// (встройка не считается).
func ExtractGPU(text string) string {
	if m := gpuRTXARe.FindStringSubmatch(text); m != nil {
		return "RTX A" + m[1]
	}
	if m := gpuRTXProRe.FindStringSubmatch(text); m != nil {
		return "RTX PRO " + m[1]
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
	if m := gpuBareRTXARe.FindStringSubmatch(text); m != nil {
		return "RTX A" + m[1]
	}
	if m := gpuBareKRe.FindStringSubmatch(text); m != nil {
		return "Quadro K" + m[1] + "M"
	}
	if m := gpuBareMRe.FindStringSubmatch(text); m != nil {
		return "Quadro M" + strings.ToUpper(m[1])
	}
	if m := gpuBarePRe.FindStringSubmatch(text); m != nil {
		return "Quadro P" + m[1]
	}
	if m := gpuTSeriesRe.FindStringSubmatch(text); m != nil {
		return "T" + m[1]
	}
	if m := gpuRadeonProRe.FindStringSubmatch(text); m != nil {
		return "Radeon Pro " + m[1]
	}
	if m := gpuArcRe.FindStringSubmatch(text); m != nil {
		sku := strings.ToUpper(strings.ReplaceAll(m[1], " ", ""))
		return "Arc " + sku
	}
	return ""
}

// LooksIntegratedGPU возвращает true, когда текст явно говорит только о
// встроенной графике. При любом признаке дискретной видеокарты возвращаем false:
// такие лоты лучше добрать Gemini, чем тихо потерять.
func LooksIntegratedGPU(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if ExtractGPU(text) != "" || discreteGPUHintRe.MatchString(text) {
		return false
	}
	return integratedGPUTextRe.MatchString(text)
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
