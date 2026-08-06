// Package filters — детерминированные слои воронки (PLAN_v4 §3.2–3.3):
// L1 — фильтр магазинов, L2 — фильтр хлама. Чистые функции без I/O:
// решения воспроизводимы и проверяются на золотой выборке (cmd/filtertest).
package filters

import (
	"regexp"
	"strings"
)

// MarkersVersion — версия словарей. Любое изменение словарей/весов меняет
// версию и требует регрессионного прогона cmd/filtertest.
const MarkersVersion = 2

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
