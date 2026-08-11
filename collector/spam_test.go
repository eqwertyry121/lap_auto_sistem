package collector

import "testing"

func TestIsSpamCommercialSellerNames(t *testing.T) {
	for _, seller := range []string{
		"Pc Komponente Servis Računara",
		"Pc Service - Grobyte",
		"It-Zona",
		"Techno-Zona Rs/Ru/Eng",
		"Dejan/Servis-Prodaja/Pc/Laptop",
		"K@Milan It Servis I Prodaja",
		"Western Europe",
	} {
		if spam, reason := IsSpam("Laptop i5", seller); !spam {
			t.Errorf("IsSpam seller %q = false, reason=%q", seller, reason)
		}
	}
}

func TestIsSpamBatchTitles(t *testing.T) {
	for _, title := range []string{
		"Laptopovi odmah spremni za rad",
		"Imamo vise modela na stanju",
		"Sva roba je nova i neotpakovana",
	} {
		if spam, reason := IsSpam(title, "Milan"); !spam {
			t.Errorf("IsSpam title %q = false, reason=%q", title, reason)
		}
	}
}

func TestIsSpamPrivateSellerPasses(t *testing.T) {
	if spam, reason := IsSpam("Lenovo ThinkPad T480 i5 16GB 512GB", "Milan"); spam {
		t.Fatalf("private seller flagged as spam: %s", reason)
	}
}
