package filters

import (
	"strings"
	"testing"
)

// Реальный мультилистинг из tools/eds_sample.json (магазин «Polovni Laptopovi»):
// плоский текст после stripHTML (теги → пробелы).
const realShopMultilisting = `✨ GARANCIJA na ispravnost 12 meseci! ✨ MOGUĆNOST ZAMENE ILI ODUSTANKA od kupovine u roku od 30 dana! ✨ MOGUĆNOST PLAĆANJA PUTEM FAKTURE ✨ Brza dostava širom Srbije! Imamo više modela na stanju. Kliknite na "SVI OGLASI" (telefon) Kliknite na "KP IZLOG" (računar) POUZDANI POLOVNI LAPTOPOVI NOVI SAD NAJTRAŽENIJI MODELI • HP EliteBook 840 G6 | i5-8265U | 16GB | 512GB SSD | 235€ • Lenovo ThinkPad T490 | i7-8665U | 16GB | 512GB SSD | 270€ • Dell Vostro 15.6" | i5-1135G7 | 16GB | 256GB SSD | 300€ OSTALI MODELI IZ PONUDE: #01-Odlican laptop Apple MacBook Pro M2 16GB 1TB 13" A2338-850€ #05-Lenovo thinkpad t15 15.6" 4K ekran i7/32gb dve grafike-650€ #12-Dell latitude 15.6" intel i5-10210U 16GB RAM 512GB SSD-300€`

func TestL1_KPMarksAreShop(t *testing.T) {
	// PLAN_v8 (2026-08-06, решение пользователя): честные метки KP снова
	// режут. is_trader больше не врёт — флаг ставится только при title
	// «Trgovac» (заявленный торговец), регрессия в models/models_test.go.
	// Решение 2026-08-05 «метки не использовать» отменено.
	if v := L1(AdFacts{IsTrader: true, Title: "Laptop i5", Description: "Prodajem svoj laptop."}); v.Class != ClassShop {
		t.Errorf("IsTrader=true (заявленный торговец) должен резать: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{KPIzlog: true, Title: "Laptop i5", Description: "Prodajem svoj laptop."}); v.Class != ClassShop {
		t.Errorf("KPIzlog=true (витрина) должен резать: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL1_RealMultilisting(t *testing.T) {
	v := L1(AdFacts{Title: "Laptopovi odmah spremni za rad +GARANCIJA 12 meseci Novi Sad",
		Description: realShopMultilisting})
	if v.Class != ClassShop {
		t.Fatalf("реальный мультилистинг не распознан: %s (%v)", v.Class, v.Reasons)
	}
	// Причина должна ссылаться на прайс-лист (М4) или маркеры (М5).
	joined := strings.Join(v.Reasons, "; ")
	if !strings.Contains(joined, "М4") && !strings.Contains(joined, "М5") {
		t.Errorf("неожиданные причины: %v", v.Reasons)
	}
}

// Реальный лот 194319150 (магазин «Best Buy 021», новая техника под заказ) —
// проходил L1 до маркеров версии 2.
const realCatalogShop = `BEST BUY - GARANTOVANO NAJBOLJA KUPOVINA Garancija 1-5 Godine! (koju resavate preko nas) Sve ide po porudzbini! Sva roba je nova i neotpakovana, u fabrickom pakovanju. Isporuka od 3 do 7 dana. Moguce licno preuzimanje ili slanje kurirskom sluzbom. Molim Vas, kontaktirajte me za aktuelne cene, posto se cesto menjaju. Mogucnost nabavke artikala koji nisu na mojim oglasima. Kontakt preko KP Poruka`

func TestL1_CatalogShop(t *testing.T) {
	v := L1(AdFacts{Title: "LENOVO Legion Pro 7 16IAX10H 83F500RBHV", Description: realCatalogShop})
	if v.Class != ClassShop {
		t.Fatalf("каталожный магазин не распознан: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL1_PrivateSellerPasses(t *testing.T) {
	// Типичный частник: одна машина, без маркеров.
	v := L1(AdFacts{
		Title:       "Lenovo ThinkPad T480 i5-8250U 8GB 256GB SSD",
		Description: "Prodajem laptop, kupljen 2019. godine, ocuvan, baterija drzi oko 3 sata. Licno preuzimanje Novi Beograd.",
		SellerAds:   1, SellerAgeDays: 800,
	})
	if v.Class == ClassShop {
		t.Fatalf("частник ошибочно срезан: %v", v.Reasons)
	}
}

func TestL1_BehavioralSignals(t *testing.T) {
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerAds: 5}); v.Class == ClassShop {
		t.Errorf("5 лотов без других признаков не должны резать: %v", v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerAds: 12}); v.Class != ClassShop {
		t.Errorf("12 лотов должны резать как перекуп/магазин: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerAds: 6}); v.Class == ClassShop {
		t.Errorf("средний объём без доп. сигналов не должен резать: %v", v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerAds: 6, IsRenewed: true}); v.Class != ClassShop {
		t.Errorf("средний объём + автообновление должен резать: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerAds: 4, SellerAgeDays: 20}); v.Class == ClassShop {
		t.Errorf("молодой аккаунт сам по себе не должен резать: %v", v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerRecentAds: 8}); v.Class != ClassShop {
		t.Errorf("8 свежих лотов должны резать как активного перекупа: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerRecentAds: 4}); v.Class == ClassShop {
		t.Errorf("4 свежих лота без других сигналов не должны резать: %v", v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Description: "Prodajem svoj laptop.", SellerRecentAds: 4, IsRenewed: true}); v.Class != ClassShop {
		t.Errorf("4 свежих лота + автообновление должны резать: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL1_SellerHistoryAndName(t *testing.T) {
	if v := L1(AdFacts{Title: "Laptop i5", SellerTraderSeen: true}); v.Class != ClassShop {
		t.Errorf("история Trgovac должна резать: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", Seller: "Laptop Centar NS"}); v.Class != ClassShop {
		t.Errorf("коммерческое имя продавца должно резать: %s (%v)", v.Class, v.Reasons)
	}
	for _, seller := range []string{
		"Pc Komponente Servis Računara",
		"Pc&Tehnika",
		"Western Europe",
		"Pc Service - Grobyte",
		"It-Zona",
		"Techno-Zona Rs/Ru/Eng",
		"Dejan/Servis-Prodaja/Pc/Laptop",
		"K@Milan It Servis I Prodaja",
	} {
		if v := L1(AdFacts{Title: "Laptop i5", Seller: seller}); v.Class != ClassShop {
			t.Errorf("коммерческое имя продавца %q должно резать: %s (%v)", seller, v.Class, v.Reasons)
		}
	}
}

func TestL1_ManualSellerLabels(t *testing.T) {
	if v := L1(AdFacts{Title: "Laptop i5", SellerLabel: "SHOP"}); v.Class != ClassShop {
		t.Fatalf("manual SHOP label must block seller: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", SellerLabel: "PRIVATE", SellerAds: 99}); v.Class != ClassPrivate {
		t.Fatalf("manual PRIVATE label must bypass behavioral heuristics: %s (%v)", v.Class, v.Reasons)
	}
	if v := L1(AdFacts{Title: "Laptop i5", SellerLabel: "PRIVATE", IsTrader: true}); v.Class != ClassShop {
		t.Fatalf("manual PRIVATE label must not override current KP trader flag: %s (%v)", v.Class, v.Reasons)
	}
}

// Реальный перекуп дня 2026-08-06 (лот 194371772, Acer VX15): без меток KP,
// без маркеров v2, но самовывоз в 5 городах + маркетинговый суперлатив.
const realResellerMulticity = `TECH SPECS (VX15) Operating System: Windows 10 Home Processor: Intel Core i5-7300HQ Memory: 12GB DDR4 prodaje se ispravan laptop, instaliran je win 10, laptop tragovi koristenja, vidi slike baterija dobra, healt 77 procenata, drzi oko 2.5 sata prodaje se sa punjacem acer, ukoliko zelite bez punjaca cena je 200e moguce licno preuzimanje u dole navedene gradove BEOGRAD ZRENJANIN KIKINDA NOVI SAD SUBOTICA . . NAJPOVOLJNIJA CENA LAPTOPA ZA OVAKVU KONFIGURACIJU . .`

func TestL1_ResellerMulticity(t *testing.T) {
	v := L1(AdFacts{Title: "Acer Aspire VX15 VX5-591G Core i5-7300HQ GTX 1050 Ti Laptop",
		Description: realResellerMulticity})
	if v.Class != ClassShop {
		t.Fatalf("перекуп с доставкой по 5 городам не распознан: %s (%v)", v.Class, v.Reasons)
	}
	joined := strings.Join(v.Reasons, "; ")
	if !strings.Contains(joined, "М7") {
		t.Errorf("среди причин нет М7 (мультигород): %v", v.Reasons)
	}
}

const westernEuropeTemplate = `Clanovi sa 0 ocena, bez upisanog broja telefona, moraju pozvati, na poruke putem Kupujem Prodajem ne odgovaramo! Pozovite i pitajte sve sto vas interesuje ▶️Western Europe◀️ Microsoft Surface Laptop 3 Top fabricko stanje -Intel i5-1035G7 -8gb ddr4 -256gb SSD NVMe perfektan`

func TestL1_WesternEuropeTemplate(t *testing.T) {
	v := L1(AdFacts{Title: "Surface Laptop 3 i5 8gb", Description: westernEuropeTemplate})
	if v.Class != ClassShop {
		t.Fatalf("шаблон Western Europe не распознан: %s (%v)", v.Class, v.Reasons)
	}
	joined := strings.Join(v.Reasons, "; ")
	if !strings.Contains(joined, "bez upisanog broja telefona") || !strings.Contains(joined, "ne odgovaramo") {
		t.Errorf("причины не показывают шаблон связи: %v", v.Reasons)
	}
}

func TestL1_PluralMultiListingTitle(t *testing.T) {
	v := L1(AdFacts{Title: "Gaming Laptopovi Acer, Lenovo", Description: "Prodajem vise modela"})
	if v.Class != ClassShop {
		t.Fatalf("plural/multi-brand title должен резать мультилистинг: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL1_MulticityAloneNotEnough(t *testing.T) {
	// М7 даёт вес 1.0 — без второго маркера порог 2.0 не берётся.
	v := L1(AdFacts{Title: "Laptop",
		Description: "Licno preuzimanje: BEOGRAD, ZRENJANIN, KIKINDA, SUBOTICA."})
	if v.Class == ClassShop {
		t.Errorf("одного мультигорода должно не хватать: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL1_SingleMarkerNotEnough(t *testing.T) {
	// Один маркер («garancija») ниже порога 2.0 — лот не режется.
	v := L1(AdFacts{Title: "Laptop i5 garancija", Description: ""})
	if v.Class == ClassShop {
		t.Errorf("одного маркера недостаточно: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL1_DiacriticsNormalization(t *testing.T) {
	// «saobražnost» с диакритикой и без должны ловиться одинаково.
	for _, text := range []string{"ide zakonska saobražnost 12 meseci i garancija",
		"ide zakonska saobraznost 12 meseci i garancija"} {
		v := L1(AdFacts{Description: text})
		if v.Class != ClassShop {
			t.Errorf("диакритика %q: got %s (%v)", text, v.Class, v.Reasons)
		}
	}
}

// ---------- L2 ----------

func TestL2_PartsOnly(t *testing.T) {
	cases := []string{
		"Prodajem laptop za delove, ne radi",
		"Laptop neispravan, ekran pukao, za rezervne delove",
		"Lenovo T450 ne pali se",
		"HP EliteBook faulty motherboard",
		"Dell E7450 i7 5600U Delovi",
		"Matična ploča Asus VivoBook S15 X530F",
	}
	for _, title := range cases {
		v := L2(AdFacts{Title: title})
		if v.Class != JunkPartsOnly {
			t.Errorf("%q: got %s, want PARTS_ONLY", title, v.Class)
		}
	}
}

func TestL2_ConditionBroken(t *testing.T) {
	v := L2(AdFacts{Condition: "broken", Title: "Laptop"})
	if v.Class != JunkPartsOnly {
		t.Errorf("condition=broken: got %s", v.Class)
	}
}

func TestL2_Defect(t *testing.T) {
	v := L2(AdFacts{Title: "ThinkPad T480", Description: "Sve radi, samo baterija ne drzi vise od 20 minuta."})
	if v.Class != JunkDefect {
		t.Errorf("дефект батареи: got %s (%v)", v.Class, v.Reasons)
	}
}

func TestL2_NeRadiTastaturaIsDefect(t *testing.T) {
	v := L2(AdFacts{Title: "Laptop ne radi tastatura"})
	if v.Class != JunkDefect {
		t.Fatalf("ne radi tastatura: got %s (%v), want DEFECT", v.Class, v.Reasons)
	}
}

func TestL2_ManualAdLabels(t *testing.T) {
	if v := L2(AdFacts{Title: "Clean laptop", AdLabel: "JUNK"}); v.Class != JunkPartsOnly {
		t.Fatalf("manual JUNK label must block ad: %s (%v)", v.Class, v.Reasons)
	}
	if v := L2(AdFacts{Title: "Laptop ne radi tastatura", AdLabel: "CLEAN"}); v.Class != JunkClean {
		t.Fatalf("manual CLEAN label must override text markers: %s (%v)", v.Class, v.Reasons)
	}
	if v := L2(AdFacts{Title: "Laptop", Condition: "broken", AdLabel: "CLEAN"}); v.Class != JunkPartsOnly {
		t.Fatalf("manual CLEAN label must not override KP condition=broken: %s (%v)", v.Class, v.Reasons)
	}
}

func TestL2_CleanPasses(t *testing.T) {
	v := L2(AdFacts{Title: "Lenovo ThinkPad T480 i5-8250U", Description: "Očuvan laptop, sve radi kako treba."})
	if v.Class != JunkClean {
		t.Errorf("чистый лот: got %s (%v)", v.Class, v.Reasons)
	}
}

func TestL2_PriceCrossCheck(t *testing.T) {
	// Маркер «ne radi» + цена внутри распределения (≥60% медианы) →
	// сомнительный маркер, ручная проверка вместо тихого блока.
	v := L2(AdFacts{Title: "Laptop ne radi", PriceEUR: 280, MedianEUR: 300})
	if v.Class != JunkUncertain {
		t.Errorf("кросс-чек: got %s, want BROKEN_UNCERTAIN", v.Class)
	}
	// Тот же маркер с ценой трупа → PARTS_ONLY.
	v = L2(AdFacts{Title: "Laptop ne radi", PriceEUR: 50, MedianEUR: 300})
	if v.Class != JunkPartsOnly {
		t.Errorf("цена трупа: got %s, want PARTS_ONLY", v.Class)
	}
}

func TestL2ReasonsAreReadableUTF8(t *testing.T) {
	cases := []Verdict{
		L2(AdFacts{Title: "Laptop", Description: "baterija ne drzi"}),
		L2(AdFacts{Title: "Laptop za delove", PriceEUR: 280, MedianEUR: 300}),
		L2(AdFacts{Title: "Laptop za delove", PriceEUR: 50, MedianEUR: 300}),
		L2(AdFacts{Title: "Laptop ne radi"}),
		L2(AdFacts{Title: "Laptop ne radi", PriceEUR: 280, MedianEUR: 300}),
	}
	bad := []string{"Рґ", "Рј", "С€", "С†", "в‚", "вЂ", "В«", "В»", "в‰"}
	for _, v := range cases {
		joined := strings.Join(v.Reasons, "; ")
		if joined == "" {
			t.Fatalf("expected diagnostic reason for %s", v.Class)
		}
		for _, marker := range bad {
			if strings.Contains(joined, marker) {
				t.Fatalf("reason contains mojibake marker %q: %q", marker, joined)
			}
		}
	}
}

func TestL2_BezPunjacaContext(t *testing.T) {
	// Регрессия кейса VX15 (2026-08-06): «prodaje se sa punjacem … ukoliko
	// zelite bez punjaca cena je 200e» — зарядка В КОМПЛЕКТЕ, «без зарядки» —
	// условный вариант со скидкой, а не дефект.
	v := L2(AdFacts{Title: "Acer Aspire VX15",
		Description: "prodaje se sa punjacem acer, ukoliko zelite bez punjaca cena je 200e"})
	if v.Class == JunkDefect {
		t.Errorf("зарядка в комплекте — не дефект: %s (%v)", v.Class, v.Reasons)
	}
	// Только условный оборот без «sa punjacem» — тоже не дефект.
	v = L2(AdFacts{Title: "Laptop", Description: "ukoliko zelite bez punjaca, cena je niza"})
	if v.Class == JunkDefect {
		t.Errorf("условное «без зарядки» — не дефект: %s (%v)", v.Class, v.Reasons)
	}
	// Честное «без зарядки» без контекста — дефект.
	v = L2(AdFacts{Title: "Laptop", Description: "prodajem laptop bez punjaca, izgubljen"})
	if v.Class != JunkDefect {
		t.Errorf("честное «без зарядки» должно быть дефектом: %s (%v)", v.Class, v.Reasons)
	}
}

func TestNormalize(t *testing.T) {
	if normalize("Šta je Čašćenje") != "sta je cascenje" {
		t.Errorf("normalize: %q", normalize("Šta je Čašćenje"))
	}
}

// ---------- бан-лист (PLAN_v5, Фаза A) ----------

func TestIsBannedModel(t *testing.T) {
	banned := []string{"macbook"}
	cases := []struct {
		text    string
		want    bool
		wantHit string
	}{
		{"MacBook Air 13 M2 8GB", true, "macbook"},
		{"APPLE MACBOOK PRO 2019 16\"", true, "macbook"},
		{"Prodajem macbook pro, ocuvan", true, "macbook"},
		{"Lenovo ThinkPad T480 i5-8250U", false, ""},
		{"HP EliteBook 840 G6", false, ""},
		{"", false, ""},
	}
	for _, c := range cases {
		got, hit := IsBannedModel(c.text, banned)
		if got != c.want || hit != c.wantHit {
			t.Errorf("IsBannedModel(%q) = (%v, %q), хочу (%v, %q)", c.text, got, hit, c.want, c.wantHit)
		}
	}
}

func TestIsBannedModel_MultipleMarkers(t *testing.T) {
	banned := []string{"macbook", "iphone"}
	if ok, hit := IsBannedModel("iPhone 13 Pro 128GB", banned); !ok || hit != "iphone" {
		t.Errorf("второй маркер: (%v, %q)", ok, hit)
	}
	// пустые/пробельные маркеры игнорируются
	if ok, _ := IsBannedModel("Lenovo T480", []string{"", "  "}); ok {
		t.Error("пустой маркер не должен банить")
	}
}
