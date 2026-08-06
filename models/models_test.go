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
	var f FlexFloat
	if err := json.Unmarshal([]byte(`"1.299,00"`), &f); err != nil {
		t.Fatalf("строка: %v", err)
	}
	if float64(f) != 1.299 {
		t.Errorf("из строки: %v", f)
	}
	if err := json.Unmarshal([]byte(`250`), &f); err != nil {
		t.Fatalf("число: %v", err)
	}
	if float64(f) != 250 {
		t.Errorf("из числа: %v", f)
	}
}
