package collector

import "strings"

// Маркеры коммерческих аккаунтов (проверяются по имени продавца).
var sellerSpamMarkers = []string{
	"store", "shop", "doo", "d.o.o", "laptop centar", "centar laptopa", "komerc", "trade",
}

// Маркеры партий товара (проверяются по заголовку).
var batchTitleMarkers = []string{
	"na stanju", "komada", "lager", "veleprodaja", "maloprodaja", "laptopovi",
}

// IsSpam отсеивает магазины и партии. Возвращает флаг и причину.
func IsSpam(title, seller string) (bool, string) {
	ls := strings.ToLower(seller)
	for _, m := range sellerSpamMarkers {
		if strings.Contains(ls, m) {
			return true, "коммерческий продавец («" + m + "»)"
		}
	}
	lt := strings.ToLower(title)
	for _, m := range batchTitleMarkers {
		if strings.Contains(lt, m) {
			return true, "партия товара («" + m + "»)"
		}
	}
	return false, ""
}
