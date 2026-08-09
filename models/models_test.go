package models

import (
	"encoding/json"
	"testing"
)

// Регрессия: живой KP отдаёт user.reviews и числом, и строкой — оба вида
// должны разбираться, иначе детализация лота падает навсегда.
func TestAdDetail_ReviewsFlex(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{"число", `{"success":true,"info":{"ad_id":1,"user":{"name":"Pera","reviews":389}}}`, 389},
		{"строка", `{"success":true,"info":{"ad_id":2,"user":{"name":"Pera","reviews":"127"}}}`, 127},
		{"пусто", `{"success":true,"info":{"ad_id":3,"user":{"name":"Pera"}}}`, 0},
	}
	for _, c := range cases {
		var dr DetailResponse
		if err := json.Unmarshal([]byte(c.raw), &dr); err != nil {
			t.Fatalf("%s: разбор: %v", c.name, err)
		}
		if dr.Info == nil {
			t.Fatalf("%s: пустой info", c.name)
		}
		if got := int64(dr.Info.User.Reviews); got != c.want {
			t.Errorf("%s: reviews=%d, хочу %d", c.name, got, c.want)
		}
	}
}

func TestFlexFloat(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{
		{`"1.299,00"`, 1299},
		{`"2 500"`, 2500},
		{`"€220"`, 220},
		{`"1,299.00"`, 1299},
		{`250`, 250},
	}
	for _, c := range cases {
		var f FlexFloat
		if err := json.Unmarshal([]byte(c.raw), &f); err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if float64(f) != c.want {
			t.Errorf("%s: got %v, want %v", c.raw, f, c.want)
		}
	}
}

// Регрессия аудита 2026-08-06: KP присылает объект trader ВСЕМ продавцам —
// магазинам с title «Trgovac», частникам с «Nije trgovac». Старая проверка
// «поле присутствует» помечала магазином 100% лотов (живые сэмплы: магазин
// Polovni Laptopovi и частники Joker/Student из tools/traderprobe).
func TestAdDetail_TraderSemantics(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"магазин Trgovac", `{"success":true,"info":{"ad_id":1,"user":{"name":"Shop","trader":{"title":"Trgovac"}}}}`, true},
		{"частник Nije trgovac", `{"success":true,"info":{"ad_id":2,"user":{"name":"Joker","trader":{"title":"Nije trgovac"}}}}`, false},
		{"поля нет", `{"success":true,"info":{"ad_id":3,"user":{"name":"Pera"}}}`, false},
		{"пустой title", `{"success":true,"info":{"ad_id":4,"user":{"name":"Pera","trader":{"title":""}}}}`, false},
		{"регистр и пробелы", `{"success":true,"info":{"ad_id":5,"user":{"name":"Shop","trader":{"title":" trgovac "}}}}`, true},
	}
	for _, c := range cases {
		var dr DetailResponse
		if err := json.Unmarshal([]byte(c.raw), &dr); err != nil {
			t.Fatalf("%s: разбор: %v", c.name, err)
		}
		if dr.Info == nil {
			t.Fatalf("%s: пустой info", c.name)
		}
		if got := dr.Info.IsTrader(); got != c.want {
			t.Errorf("%s: IsTrader()=%v, хочу %v", c.name, got, c.want)
		}
	}
}
