package specs

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GeminiSpecs — характеристики, извлечённые Gemini из текста/фото (L3,
// ступени 2–3 воронки). Единый формат для живого бота и cmd/gemini-enrich.
type GeminiSpecs struct {
	LaptopModel string `json:"laptop_model"` // точная модель ноутбука (шильдик/гравировка), если видна
	CPU         string `json:"cpu"`
	RAMGB       int    `json:"ram_gb"`
	SSDGB       int    `json:"ssd_gb"`
	GPU         string `json:"gpu"`        // дискретная GPU; «integrated» = только встройка
	WhyNoCPU    string `json:"why_no_cpu"` // обязательна при пустом cpu: почему не определён
}

// Empty — ничего не извлечено.
func (s GeminiSpecs) Empty() bool {
	return strings.TrimSpace(s.CPU) == "" && s.RAMGB == 0 && s.SSDGB == 0 && strings.TrimSpace(s.GPU) == ""
}

// GPUIntegrated — GPU явно отмечен как встроенный (дискретной нет).
// Такие ответы в ценовые группы не попадают (встройка не удорожает лот),
// но показываются в строке конфигурации и ресерче.
func (s GeminiSpecs) GPUIntegrated() bool {
	g := strings.ToLower(strings.TrimSpace(s.GPU))
	switch g {
	case "integrated", "integrated graphics", "integrisana", "integrisana grafika",
		"встроенная", "встроенная видеокарта", "встройка":
		return true
	}
	for _, marker := range []string{
		"intel hd", "intel uhd", "intel iris", "iris xe", "intel arc", "arc 130v", "arc 140v",
		"amd radeon graphics", "radeon graphics", "radeon 610m", "radeon 660m",
		"radeon 680m", "radeon 760m", "radeon 780m", "radeon 860m", "radeon 880m", "radeon 890m",
	} {
		if strings.Contains(g, marker) {
			return true
		}
	}
	return false
}

// GeminiSpecsPrompt — системный промпт извлечения характеристик. Задача —
// собрать с текста/фото МАКСИМУМ информации о ноутбуке (ТЗ: фото → модель →
// ресерч). Ответ — строго JSON; правила квалификации CPU совпадают с ТЗ.
const GeminiSpecsPrompt = `Определи характеристики ноутбука по объявлению (текст и/или фотографии: наклейки Intel/AMD, шильдики на дне, гравировки модели на крышке и рамке экрана). Собери максимум информации о ноутбуке. Верни строго один JSON-объект без какого-либо текста вокруг:
{"laptop_model": "точная модель ноутбука (например Lenovo ThinkPad T480, HP EliteBook 840 G6) или пустая строка", "cpu": "точная модель CPU с поколением (например i5-1135G7, Ryzen 5 4600H, Ultra 7 155H) или пустая строка", "ram_gb": число или 0, "ssd_gb": число или 0, "gpu": "модель дискретной видеокарты (RTX 3060, RX 6600M), либо \"integrated\" если есть только встроенная графика, либо пустая строка если не видно", "why_no_cpu": "если cpu пуст — обязательно одна фраза почему (нет зацепок для определения модели, фото размыты, наклейки не видно и т.п.); иначе пустая строка"}
Правила:
1. cpu — ТОЛЬКО точная модель. «i5», «i7», «Intel Core», «Ryzen» без номера модели — пустая строка (и заполни why_no_cpu). Цвет/серия наклейки Intel/AMD, форма корпуса и семейство ноутбука НЕ являются доказательством точного CPU: не выводи CPU по ним, только упомяни такую зацепку в why_no_cpu.
2. laptop_model — сначала проверь заголовок и описание: если там явно написано что-то вроде «Lenovo ThinkPad E14 Gen 6», запиши это. Потом ищи шильдик на дне ноутбука, гравировку на крышке или рамке экрана, наклейки серии. Если видно только семейство (например «EliteBook») без поколения — пиши что видно.
3. gpu — если дискретной видеокарты нет (Intel HD/UHD/Iris, Radeon Graphics), пиши "integrated".
4. Не выдумывай: если характеристика явно не указана — 0 или пустая строка.
5. 1TB считай как 1024 GB.`

// ParseGeminiSpecs — терпимый разбор ответа: снимает markdown-заборы и
// вырезает JSON-объект из возможного словесного мусора вокруг.
func ParseGeminiSpecs(raw string) (GeminiSpecs, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	start := strings.Index(s, "{")
	if start < 0 {
		return GeminiSpecs{}, fmt.Errorf("нет JSON в ответе: %s", truncateSpecs(s, 120))
	}
	var sp GeminiSpecs
	if err := json.NewDecoder(strings.NewReader(s[start:])).Decode(&sp); err != nil {
		return GeminiSpecs{}, fmt.Errorf("разбор JSON: %w", err)
	}
	return sp, nil
}

func truncateSpecs(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
