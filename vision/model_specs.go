package vision

import (
	"context"
	"fmt"
	"strings"

	"kpbot/specs"
)

// Интернет-ступень L3.4 «модель→железо» (PLAN_v5, Фаза C): когда модель
// ноутбука известна (из текста или по фото), но CPU или GPU так и не
// определены, Gemini с Google Search ищет в интернете характеристики этой
// модели (ЧТО за ноутбук, а не сколько он стоит — цены только из данных KP).
// Найденные значения валидируются по hw.db уже в воронке (нет балла — не принято).

const modelSpecsSystemPrompt = `Ты — эксперт по ноутбукам. Пользуясь поиском в интернете, определи характеристики модели ноутбука.
Правила:
1. cpu — ТОЛЬКО точная модель с поколением (i5-1135G7, Ryzen 5 4600H, Ultra 7 155H). Если у модели или семейства несколько CPU/GPU-конфигураций и нет точного SKU/MTM/part number, НЕ выбирай "самую вероятную": оставь неоднозначные поля пустыми и объясни это в why_no_cpu.
2. gpu — модель ДИСКРЕТНОЙ видеокарты (RTX 3060, GTX 1650, Radeon RX 6600M). Если дискретной нет — "integrated".
ОТВЕТ — строго один JSON-объект без какого-либо текста вокруг:
{"laptop_model": "уточнённое полное название модели", "cpu": "точная модель CPU или пустая строка", "ram_gb": число или 0, "ssd_gb": число или 0, "gpu": "дискретная GPU или integrated или пустая строка", "why_no_cpu": "если cpu пуст — почему; иначе пустая строка"}`

// ModelSpecsResearch — поиск характеристик модели ноутбука в интернете.
// Возвращает спеки и отправленный промпт/сырой ответ (для трассировки).
func ModelSpecsResearch(ctx context.Context, gem *GeminiClient, model, title, priceNote string) (specs.GeminiSpecs, string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Модель ноутбука: %s\n", model)
	if title != "" && title != model {
		fmt.Fprintf(&b, "Заголовок объявления: %s\n", title)
	}
	if priceNote != "" {
		fmt.Fprintf(&b, "Дополнительно из объявления: %s\n", priceNote)
	}
	b.WriteString("Найди в интернете характеристики этой модели и верни JSON.")
	prompt := b.String()

	raw, _, err := gem.GenerateWithSearch(ctx, modelSpecsSystemPrompt, []Part{{Text: prompt}})
	if err != nil {
		return specs.GeminiSpecs{}, prompt, "", err
	}
	gs, perr := specs.ParseGeminiSpecs(raw)
	return gs, prompt, raw, perr
}
