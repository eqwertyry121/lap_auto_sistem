package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"

	"kpbot/filters"
)

// markerReport — частотный анализ описаний (PLAN_v4, Фаза 2, веха 2.2):
// когорта «магазины» (is_trader ∨ kp_izlog) против когорты «частники»;
// 1–3-граммы с lift и поддержкой. Кандидаты в словарь маркеров L1 —
// в CSV и на экран; финальный отбор — за человеком (веха 2.3).
func markerReport(ctx context.Context, dbPath, csvPath string, minLift, minSupportPct float64) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Println("датасет:", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
SELECT description, is_trader, kp_izlog FROM research_ads
WHERE fetch_status='OK' AND description != ''`)
	if err != nil {
		fmt.Println("выборка:", err)
		os.Exit(1)
	}
	defer rows.Close()

	// df — документная частота n-грамм по когортам: в скольких описаниях
	// когорты встречается грамма (устойчивее сырых счётчиков).
	dfShop := map[string]int{}
	dfPriv := map[string]int{}
	var nShop, nPriv int
	for rows.Next() {
		var desc string
		var trader, izlog int
		if err := rows.Scan(&desc, &trader, &izlog); err != nil {
			fmt.Println("scan:", err)
			os.Exit(1)
		}
		grams := ngrams(filters.Normalize(desc), 3)
		if trader == 1 || izlog == 1 {
			nShop++
			for g := range grams {
				dfShop[g]++
			}
		} else {
			nPriv++
			for g := range grams {
				dfPriv[g]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		fmt.Println("выборка:", err)
		os.Exit(1)
	}

	fmt.Printf("=== ЧАСТОТНЫЙ АНАЛИЗ ОПИСАНИЙ (Фаза 2) ===\n")
	fmt.Printf("Когорты: магазины = %d описаний, частники = %d описаний\n", nShop, nPriv)
	if nShop < 50 || nPriv < 50 {
		fmt.Printf("⚠️ выборка мала — кандидаты ориентировочные, ждать докачки (Фаза 1)\n")
	}
	if nShop == 0 || nPriv == 0 {
		fmt.Println("Одна из когорт пуста — lift не вычисляется (нечему противопоставлять).")
		fmt.Println("Анализ повторить, когда появятся описания обеих когорт.")
		return
	}

	type candidate struct {
		gram         string
		sShop, sPriv float64 // поддержка, %
		lift         float64
	}
	const eps = 0.001
	var cands []candidate
	for g, fs := range dfShop {
		sShop := 100 * float64(fs) / math.Max(float64(nShop), 1)
		if sShop < minSupportPct {
			continue
		}
		fp := dfPriv[g]
		sPriv := 100 * float64(fp) / math.Max(float64(nPriv), 1)
		lift := (sShop/100 + eps) / (sPriv/100 + eps)
		if lift < minLift {
			continue
		}
		cands = append(cands, candidate{gram: g, sShop: sShop, sPriv: sPriv, lift: lift})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].lift != cands[j].lift {
			return cands[i].lift > cands[j].lift
		}
		return cands[i].sShop > cands[j].sShop
	})

	fmt.Printf("Кандидаты маркеров (lift ≥ %.1f, поддержка магазинов ≥ %.0f%%): %d\n\n",
		minLift, minSupportPct, len(cands))
	fmt.Printf("%-38s %8s %8s %7s\n", "грамма", "магазин%", "частник%", "lift")
	for i, c := range cands {
		if i == 40 {
			fmt.Printf("… (полный список в %s)\n", csvPath)
			break
		}
		fmt.Printf("%-38s %7.1f%% %7.1f%% %7.1f\n", truncate(c.gram, 38), c.sShop, c.sPriv, c.lift)
	}

	// CSV со всеми кандидатами — вход для ручного отбора словаря.
	f, err := os.Create(csvPath)
	if err != nil {
		fmt.Println("csv:", err)
		os.Exit(1)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"ngram", "support_shop_pct", "support_priv_pct", "lift"})
	for _, c := range cands {
		_ = w.Write([]string{
			c.gram,
			fmt.Sprintf("%.1f", c.sShop),
			fmt.Sprintf("%.1f", c.sPriv),
			fmt.Sprintf("%.1f", c.lift),
		})
	}
	w.Flush()
	fmt.Printf("\nCSV сохранён: %s\n", csvPath)
}

// ngrams — множество 1..maxN-грамм нормализованного текста; чисто цифровые
// граммы (цены, объёмы) пропускаются — они не маркеры стиля.
func ngrams(textNorm string, maxN int) map[string]struct{} {
	words := tokenizeWords(textNorm)
	out := make(map[string]struct{}, len(words)*2)
	for n := 1; n <= maxN; n++ {
		for i := 0; i+n <= len(words); i++ {
			g := strings.Join(words[i:i+n], " ")
			if allDigits(g) {
				continue
			}
			out[g] = struct{}{}
		}
	}
	return out
}

func tokenizeWords(s string) []string {
	var (
		words []string
		cur   strings.Builder
	)
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() > 0 {
			if w := cur.String(); len(w) >= 2 {
				words = append(words, w)
			}
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		if w := cur.String(); len(w) >= 2 {
			words = append(words, w)
		}
	}
	return words
}

func allDigits(g string) bool {
	for _, r := range g {
		if !unicode.IsDigit(r) && r != ' ' {
			return false
		}
	}
	return true
}
