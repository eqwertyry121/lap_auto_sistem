package filters

import "strings"

// IsBannedModel — мгновенный бан запрещённых линеек (PLAN_v5, Фаза A):
// пользователь не работает с этими моделями вообще (по умолчанию MacBook).
// Проверка по нормализованному тексту (нижний регистр + снятие диакритики),
// 0 запросов — вызывается ещё до /eds/.
func IsBannedModel(text string, banned []string) (bool, string) {
	norm := normalize(text)
	for _, b := range banned {
		nb := normalize(strings.TrimSpace(b))
		if nb != "" && strings.Contains(norm, nb) {
			return true, nb
		}
	}
	return false, ""
}
