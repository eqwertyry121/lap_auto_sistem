// Package filters — детерминированные слои воронки (PLAN_v4 §3.2–3.3):
// L1 — фильтр магазинов, L2 — фильтр хлама. Чистые функции без I/O:
// решения воспроизводимы и проверяются на золотой выборке (cmd/filtertest).
package filters

import (
	"fmt"
	"regexp"
	"strings"
)

// MarkersVersion — версия словарей. Любое изменение словарей/весов меняет
// версию и требует регрессионного прогона cmd/filtertest.
const MarkersVersion = 4

// normalize — нижний регистр + снятие сербской диакритики, чтобы один
// словарь ловил и «saobražnost», и «saobraznost».
func normalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 'č', 'ć':
			r = 'c'
		case 'š':
			r = 's'
		case 'ž':
			r = 'z'
		case 'đ':
			r = 'd'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Normalize — экспорт нормализации для инструментов анализа
// (cmd/analyze -markers, cmd/filtertest): словари и анализ должны
// работать в одной системе координат.
func Normalize(s string) string { return normalize(s) }

var stripHTMLTagRe = regexp.MustCompile(`<[^>]+>`)

// StripHTML — теги → пробелы, HTML-сущности → символы, пробелы
// нормализуются. Описания KP приходят в HTML; маркеры работают по
// плоскому тексту (PLAN_v4, Решение ②).
func StripHTML(s string) string {
	s = stripHTMLTagRe.ReplaceAllString(s, " ")
	for _, pair := range [][2]string{{"&nbsp;", " "}, {"&amp;", "&"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.Join(strings.Fields(s), " ")
}

// L1-маркеры магазинов (PLAN_v4 §8.1): старт из наблюдений за живыми
// описаниями, подтверждать частотным анализом (cmd/analyze -markers).
// Вес 1.0 — обычный маркер; порог срабатывания М5 — ThresholdL1.
type l1Marker struct {
	phrase string
	weight float64
	why    string
}

// ThresholdL1 — суммарный вес маркеров М5, достаточный для вердикта SHOP.
// Калибруется на золотой выборке (веха 2.6); до калибровки — консервативно.
const ThresholdL1 = 2.0

// ВАЖНО (решение 2026-08-05): сюда НЕ входят слова-элементы интерфейса KP
// («svi oglasi», «kp izlog» и т.п.) и метки Trgovac/KP Izlog — детекция
// идёт только по содержательным словам самого описания.
var l1Markers = []l1Marker{
	{"garancija", 1, "гарантия (частники почти не дают)"},
	{"garancijom", 1, "гарантия (склонение)"},
	{"vise modela", 1, "несколько моделей в одном лоте"},
	{"imamo vise", 1, "«у нас есть ещё»"},
	{"na stanju", 1, "склад/партия"},
	{"pouzdani polovni laptopovi", 1, "бренд перекупа"},
	{"faktur", 1, "оплата по счёту (юрлицо)"},
	{"dostava sirom srbije", 1, "доставка по всей Сербии"},
	{"odustanka", 1, "«право отказа» — формула магазина"},
	{"saobraznost", 1, "законная saobražnost 12 мес. — только торговцы"},
	{"maloprodaja", 1, "розница/опт"},
	{"veleprodaja", 1, "опт"},
	{"polovni laptopovi novi sad", 1, "бренд перекупа (город)"},
	// Каталог/магазин «под заказ» (стиль «Best Buy»: новая запечатанная
	// техника, цены по запросу, доставка 3–7 дней, набавка артиклов).
	{"porudzbini", 1, "«под заказ» — каталожный магазин"},
	{"fabrickom pakovanju", 1, "фабричная упаковка — новая запечатанная техника"},
	{"neotpakovana", 1, "нераспакованное (новая техника, реселлер)"},
	{"aktuelne cene", 1, "«актуальные цены» — каталог, цены меняются"},
	{"nabavke artikala", 1, "«набавка артиклов» под заказ — дропшиппинг"},
	{"sva roba je nova", 1, "«весь товар новый» — магазин новой техники"},
	// М8 — маркетинговые суперлативы (PLAN_v8). Вес 1.0: в одиночку порог
	// не берут, но в паре с М7/прочими маркерами дают SHOP. Кейс-основание:
	// перекуп VX15 (2026-08-06) «NAJPOVOLJNIJA CENA LAPTOPA ZA OVAKVU
	// KONFIGURACIJU».
	{"najpovoljnija cena", 1, "«самая выгодная цена» — маркетинг"},
	{"najpovoljnija ponuda", 1, "«самое выгодное предложение» — маркетинг"},
	{"najbolja ponuda", 1, "«лучшее предложение» — маркетинг"},
	{"najniza cena", 1, "«самая низкая цена» — маркетинг"},
}

// markerWeight — суммарный вес L1-маркеров в тексте (по нормализованному).
func markerWeight(textNorm string) (float64, []string) {
	var (
		w       float64
		reasons []string
	)
	for _, m := range l1Markers {
		if strings.Contains(textNorm, m.phrase) {
			w += m.weight
			reasons = append(reasons, "маркер «"+m.phrase+"» ("+m.why+")")
		}
	}
	// Эмодзи-витрины: два и более «✨» — оформление магазина.
	if strings.Count(textNorm, "✨") >= 2 {
		w += 1
		reasons = append(reasons, "эмодзи-витрина (✨≥2)")
	}
	// М7 — «мультигород»: самовывоз в ≥4 городах — разъездной перекуп.
	if n := countCities(textNorm); n >= citiesThreshold {
		w += 1
		reasons = append(reasons, fmt.Sprintf("М7: самовывоз в %d городах (≥%d)", n, citiesThreshold))
	}
	return w, reasons
}

// L2-словари (PLAN_v4 §3.3). ВАЖНО не смешивать классы: PARTS_ONLY — труп
// или запчасти (жёсткий блок), DEFECT — рабочий, но с дефектом (⚠️ CHECK).

// partsOnlyMarkers — продажа запчастей/нерабочего.
var partsOnlyMarkers = []string{
	"neisprav", "pokvaren", "ne radi", "neupaljiv", "za delove", "za dijelove",
	"za rezervne", "defekt", "ostecen", "broken", "faulty", "parts only",
	"za otpad", "ne pali se", "ne puni se", "ne daje sliku", "mrtav",
}

// defectMarkers — рабочий, но с дефектом (мягкий класс).
var defectMarkers = []string{
	"baterija ne drzi", "baterija slaba", "baterija crkla", "baterija traje kratko",
	"zaliven", "pukao ekran", "puknut ekran", "mrtvi pikseli", "ne radi tastatura",
	"ne radi touchpad", "bez punjaca", "bez diska", "slomljena sarka",
	"slomljen pant", "fleke na ekranu",
}

// conditionBroken — официальное поле KP «broken», если появится.
const conditionBroken = "broken"

// М7 «мультигород» (PLAN_v8): список самовывоза из ≥4 городов — поведение
// разъездного перекупа (частник указывает 0–1 город). Кейс-основание:
// VX15 (2026-08-06) — BEOGRAD, ZRENJANIN, KIKINDA, NOVI SAD, SUBOTICA.
// Слова берутся по границам слов, чтобы «nis» не ловился внутри «niska»;
// учтены падежные формы «Novi Sad».
var cityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bbeograd\w*\b`),
	regexp.MustCompile(`\bnov[io] sad\w*\b|\bnovom sadu\b|\bnovog sada\b`),
	regexp.MustCompile(`\bnis\b|\bnisu\b|\bnisa\b`),
	regexp.MustCompile(`\bkragujevac\b`),
	regexp.MustCompile(`\bsubotica\b`),
	regexp.MustCompile(`\bzrenjanin\b`),
	regexp.MustCompile(`\bkikinda\b`),
	regexp.MustCompile(`\bpancevo\b`),
	regexp.MustCompile(`\bcacak\b`),
	regexp.MustCompile(`\bkraljevo\b`),
	regexp.MustCompile(`\bleskovac\b`),
	regexp.MustCompile(`\bsombor\b`),
	regexp.MustCompile(`\bvaljevo\b`),
	regexp.MustCompile(`\bsmederevo\b`),
	regexp.MustCompile(`\bsabac\b`),
	regexp.MustCompile(`\buzice\b`),
	regexp.MustCompile(`\bvranje\b`),
	regexp.MustCompile(`\bjagodina\b`),
	regexp.MustCompile(`\bbor\b`),
	regexp.MustCompile(`\bruma\b`),
	regexp.MustCompile(`\bsremska mitrovica\b`),
	regexp.MustCompile(`\bindjija\b`),
	regexp.MustCompile(`\bvrsac\b`),
	regexp.MustCompile(`\bbacka palanka\b`),
	regexp.MustCompile(`\bzajecar\b`),
	regexp.MustCompile(`\bpirot\b`),
}

// countCities — сколько РАЗНЫХ городов из словаря упомянуто в тексте.
func countCities(textNorm string) int {
	n := 0
	for _, re := range cityPatterns {
		if re.MatchString(textNorm) {
			n++
		}
	}
	return n
}

// citiesThreshold — с скольких городов описание считается «мультигородом».
const citiesThreshold = 4
