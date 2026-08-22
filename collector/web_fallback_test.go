package collector

import (
	"encoding/json"
	"testing"
)

func TestExtractNextData(t *testing.T) {
	body := []byte(`<html><script id="__NEXT_DATA__" type="application/json">{"props":{"name":"A &amp; B"}}</script></html>`)
	raw, err := extractNextData(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	props := got["props"].(map[string]any)
	if props["name"] != "A & B" {
		t.Fatalf("name = %q", props["name"])
	}
}

func TestExtractNextDataMissing(t *testing.T) {
	if _, err := extractNextData([]byte("<html></html>")); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseCount(t *testing.T) {
	for input, want := range map[string]int64{"701": 701, "1.234": 1234, "2 345": 2345, "": 0} {
		if got := parseCount(input); got != want {
			t.Errorf("parseCount(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestNextAdPriceNumberAcceptsStringAndNumber(t *testing.T) {
	for raw, want := range map[string]float64{`{"priceNumber":"450"}`: 450, `{"priceNumber":465}`: 465} {
		var ad nextAd
		if err := json.Unmarshal([]byte(raw), &ad); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if got := float64(ad.PriceNumber); got != want {
			t.Fatalf("price for %s = %.0f, want %.0f", raw, got, want)
		}
	}
}
