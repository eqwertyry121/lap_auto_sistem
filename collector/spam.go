package collector

import "strings"

// Маркеры коммерческих аккаунтов (проверяются по имени продавца).
var sellerSpamMarkers = []string{
	"store", "shop", "doo", "d.o.o", "laptop centar", "centar laptopa", "laptop servis",
	"servis racunara", "racunari", "kompjuteri", "computer", "komerc", "trade",
	"best buy", "tehnika", "komponente", "western europe", "pc service", "it servis",
	"servis-prodaja", "it-zona", "techno-zona",
}

// Маркеры партий товара (проверяются по заголовку).
var batchTitleMarkers = []string{
	"na stanju", "komada", "lager", "veleprodaja", "maloprodaja", "laptopovi",
	"vise modela", "imamo vise", "sva roba je nova", "neotpakovana", "fabrickom pakovanju",
}

// IsSpam отсеивает магазины и партии. Возвращает флаг и причину.
func IsSpam(title, seller string) (bool, string) {
	ls := normalizeSpam(seller)
	for _, m := range sellerSpamMarkers {
		if strings.Contains(ls, m) {
			return true, "коммерческий продавец («" + m + "»)"
		}
	}
	lt := normalizeSpam(title)
	for _, m := range batchTitleMarkers {
		if strings.Contains(lt, m) {
			return true, "партия товара («" + m + "»)"
		}
	}
	return false, ""
}

func normalizeSpam(s string) string {
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
