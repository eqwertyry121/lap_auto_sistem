// models — список доступных моделей Gemini для заданного GEMINI_API_KEY.
// Помогает выбрать рабочую модель (старые могут быть недоступны новым ключам).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"kpbot/config"
)

func main() {
	cfg := config.Load()
	if cfg.GeminiAPIKey == "" {
		fmt.Println("GEMINI_API_KEY не задан")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	url := "https://generativelanguage.googleapis.com/v1beta/models?pageSize=200"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("x-goog-api-key", cfg.GeminiAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("ошибка запроса:", err)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var mr struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Description string   `json:"description"`
			InputToken  int      `json:"inputTokenLimit"`
			Supported   []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &mr); err != nil {
		fmt.Printf("HTTP %d, не удалось разобрать: %s\n", resp.StatusCode, string(raw[:min(300, len(raw))]))
		return
	}

	// Сортируем и печатаем только генеративные модели, сперва flash/flash-lite (быстрые).
	var names []string
	for _, m := range mr.Models {
		ok := false
		for _, s := range m.Supported {
			if s == "generateContent" {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		names = append(names, strings.TrimPrefix(m.Name, "models/"))
	}
	sort.Slice(names, func(i, j int) bool {
		pi := priority(names[i])
		pj := priority(names[j])
		if pi != pj {
			return pi < pj
		}
		return names[i] < names[j]
	})

	fmt.Printf("Доступно моделей с generateContent: %d\n\n", len(names))
	for _, n := range names {
		fmt.Println("  " + n)
	}
}

func priority(n string) int {
	// быстрые/дешёвые выше
	switch {
	case strings.Contains(n, "flash-lite"):
		return 0
	case strings.Contains(n, "flash"):
		return 1
	case strings.Contains(n, "pro"):
		return 2
	default:
		return 3
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
