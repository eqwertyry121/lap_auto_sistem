package vision

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"kpbot/models"
)

// systemPrompt — жёсткие правила квалификации из ТЗ (4.txt), секция про CPU.
const systemPrompt = `Ты — опытный перекупщик ноутбуков на сербском маркетплейсе KupujemProdajem. Твоя задача — по объявлению (текст, цена, рыночная статистика, фотографии) определить, можно ли купить этот ноутбук заметно ниже рынка и перепродать с прибылью.

ЖЁСТКИЕ ПРАВИЛА КВАЛИФИКАЦИИ ПРОЦЕССОРА:
1. ЗАПРЕЩЕНО ставить is_deal=true, если не определена ТОЧНАЯ модель процессора (например i5-1135G7, i7-10750H, Ryzen 5 4600H). Формулировки «i5», «i7», «Intel Core» без модели и поколения — недостаточны.
2. На фотографиях ищи наклейки Intel Core / AMD Ryzen на палмресте и шильдики на дне ноутбука:
   - серый/чёрный стикер Intel Core — обычно 10–14 поколение;
   - синий стикер Intel Core — обычно 4–9 поколение (старые и дешёвые машины).
3. Если в тексте указано просто «i5»/«i7» и по фото поколение не читается: is_deal=false, need_check=true, а reason ДОЛЖЕН начинаться со слов «Неизвестно поколение CPU».
4. Если модель ноутбука не удалось определить ни по тексту, ни по фото — model_found=false.

ПРАВИЛА ОЦЕНКИ ВЫГОДЫ:
- Сравни цену продавца с рыночным диапазоном похожих лотов (он дан во входных данных) и со своими знаниями рыночных цен.
- Учитывай дефекты из описания и с фото: трещины, залитие, битые пиксели, отсутствие SSD/памяти, «не включается», убитая батарея.
- estimated_profit = реальная рыночная цена − цена продавца − издержки на перепродажу (~10–15%). Может быть отрицательной.
- is_deal=true ТОЛЬКО если прибыль уверенно положительная (от ~€30–40 и выше) И точная модель определена.
- reason и specs пиши на русском. specs — точная модель и ключевое железо одной строкой.

ОТВЕТ — строго один JSON-объект без какого-либо текста вокруг:
{"is_deal": false, "need_check": false, "estimated_profit": 0, "reason": "…", "specs": "…", "model_found": true}`

// Evaluator — двухфазная оценка лота: текст → (при необходимости) фото.
type Evaluator struct {
	gemini    *GeminiClient
	sem       chan struct{} // семафор параллельных оценок
	maxPhotos int
}

func NewEvaluator(gemini *GeminiClient, concurrency, maxPhotos int) *Evaluator {
	if concurrency <= 0 {
		concurrency = 5
	}
	if maxPhotos <= 0 {
		maxPhotos = 5
	}
	return &Evaluator{
		gemini:    gemini,
		sem:       make(chan struct{}, concurrency),
		maxPhotos: maxPhotos,
	}
}

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// stripHTML убирает теги и нормализует пробелы (описание KP приходит в HTML).
func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	for _, pair := range [][2]string{{"&nbsp;", " "}, {"&amp;", "&"}, {"&quot;", "\""}, {"&#39;", "'"}, {"&lt;", "<"}, {"&gt;", ">"}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.Join(strings.Fields(s), " ")
}

// textPrompt — входные данные для фазы 1.
func textPrompt(l models.Listing, d *models.AdDetail, marketHint string) string {
	var b strings.Builder
	b.WriteString("Оцени объявление о продаже ноутбука.\n\n")
	fmt.Fprintf(&b, "Заголовок: %s\n", l.Title)
	fmt.Fprintf(&b, "Цена продавца: %s\n", models.FormatPrice(l.Price, l.Currency))
	if d != nil && d.Description != "" {
		fmt.Fprintf(&b, "Описание продавца: %s\n", truncate(stripHTML(d.Description), 2500))
	}
	if d != nil && len(d.Attributes) > 0 {
		b.WriteString("Характеристики из объявления:\n")
		for _, a := range d.Attributes {
			if a.Name == "" && a.Value == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s\n", a.Name, a.Value)
		}
	}
	if marketHint != "" {
		fmt.Fprintf(&b, "\nРыночная статистика: %s\n", marketHint)
	} else {
		b.WriteString("\nРыночная статистика: данных по похожим лотам пока нет.\n")
	}
	return b.String()
}

func parseVerdict(raw string) (models.Verdict, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return models.Verdict{}, fmt.Errorf("нет JSON в ответе: %s", truncate(s, 120))
	}
	var v models.Verdict
	if err := json.Unmarshal([]byte(s[start:end+1]), &v); err != nil {
		return models.Verdict{}, fmt.Errorf("разбор JSON: %w", err)
	}
	return v, nil
}

// Evaluate — полный цикл оценки одного лота. Блокирует семафор на время
// обоих вызовов Gemini, чтобы соблюсти лимит параллельности.
func (e *Evaluator) Evaluate(ctx context.Context, l models.Listing, d *models.AdDetail, marketHint string) (models.Verdict, error) {
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return models.Verdict{}, ctx.Err()
	}

	// ФАЗА 1: текст
	prompt := textPrompt(l, d, marketHint)
	out, err := e.gemini.Generate(ctx, systemPrompt, []Part{{Text: prompt}})
	if err != nil {
		return models.Verdict{}, fmt.Errorf("фаза 1 (текст): %w", err)
	}
	v, err := parseVerdict(out)
	if err != nil {
		return models.Verdict{}, fmt.Errorf("фаза 1 (текст): %w", err)
	}

	// ФАЗА 2: фото — только если модель не определена и фото есть
	if v.ModelFound || d == nil || len(d.Photos) == 0 {
		return v, nil
	}

	parts := []Part{{Text: prompt + "\n\nМодель не удалось определить по тексту. Ниже фотографии лота: внимательно рассмотри наклейки процессора, шильдики на дне и гравировки модели, затем оцени заново по тем же правилам."}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var imgParts []Part
	limit := e.maxPhotos
	if limit > len(d.Photos) {
		limit = len(d.Photos)
	}
	for i := 0; i < limit; i++ {
		u := d.Photos[i].BestURL()
		if u == "" {
			continue
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			p, err := e.gemini.ImageToPart(ctx, u)
			if err != nil {
				return
			}
			mu.Lock()
			imgParts = append(imgParts, *p)
			mu.Unlock()
		}(u)
	}
	wg.Wait()

	if len(imgParts) == 0 {
		return v, nil // фото не скачались — остаёмся с текстовым вердиктом
	}
	parts = append(parts, imgParts...)

	out, err = e.gemini.Generate(ctx, systemPrompt, parts)
	if err != nil {
		return v, fmt.Errorf("фаза 2 (vision): %w", err)
	}
	v2, err := parseVerdict(out)
	if err != nil {
		return v, fmt.Errorf("фаза 2 (vision): %w", err)
	}
	return v2, nil
}
