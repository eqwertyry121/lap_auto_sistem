package specs

import "testing"

// Заголовки — реальные из датасета KP (data/research.csv).
func TestExtractCPU(t *testing.T) {
	cases := []struct{ text, want string }{
		{"Lenovo ThinkPad T490 i7-8665U 16GB DDR4 512GB SSD nvme", "i7-8665U"},
		{"Lenovo ThinkPad 15.6 i5-8265u 16gb 512g ssd #29", "i5-8265U"},
		{"Dell 7550 i7-10750H/32GB/512GB NVMe/T2000 4GB/15.6\" FHD", "i7-10750H"},
		{"Lenovo P16 gen1/i7 12850HX/32gb/1tb/Nvidia RTX A3000 12gb/2k", "i7-12850HX"},
		{"Acer Predator Helios Neo 16 i7-13700HX/32GB DDR5/1TB/RTX4060", "i7-13700HX"},
		{"HP EliteBook 630 G9 i5-1235U 16GB 256GB SSD +GARANCIJA", "i5-1235U"},
		{"Laptop Toshiba L735/i3-330M/4gb ddr3", "i3-330M"},
		{"HP Probook 430 G7 - i5 - 10210U / 8 GB/ 256 GB", "i5-10210U"},
		{"Hp 8540p Procesor Intel Core i5 -540M CPU 2,53GHZ", "i5-540M"},
		{"Panasonic ToughBook CF-20- i5-7Y57-7gen- /8 Gb/256G", "i5-7Y57"},
		{"Laptop AMD Ryzen 5 4600H 8GB/512GB", "Ryzen 5 4600H"},
		{"HP EliteBook 855 G8 Ryzen 7 PRO 5850u 8c/16t", "Ryzen 7 PRO 5850U"},
		{"HP Victus / 4070 / R7-7840HS / 32GB", "Ryzen 7 7840HS"},
		{"Lenovo Yoga Pro 7 R7 PRO 8845HS 16G/1T", "Ryzen 7 PRO 8845HS"},
		{"NOV NEOTPAKOVAN Lenovo T14 GEN 6 - Ryzen AI 7 PRO 350", "Ryzen AI 7 PRO 350"},
		{"ASUS ProArt H7606GM-SR009XOA Ryzen AI 9 465/RTX 5060", "Ryzen AI 9 465"},
		{"ASUS ROG Zephyrus G14 AMD Ryzen AI 9 HX 370 RTX 5060", "Ryzen AI 9 HX 370"},
		{"HP Elitebook 8 G1a 14- R7 AI 350 PRO, 32GB", "Ryzen AI 7 PRO 350"},
		{"ASUS Vivobook S16 M3607KA-SH091W Ryzen AI 5 330", "Ryzen AI 5 330"},
		{"HP ZBook Studio Ultra 7 155H 32GB 1TB", "Ultra 7 155H"},
		// PLAN_v5: каталожный формат магазинов + плюс-варианты.
		{"LENOVO Legion Pro 7 Intel Core Ultra 9 Processor 290HX Plus RTX 5090", "Ultra 9 290HX Plus"},
		{"HP Elitebook 6 G1a ULTRA 5 225U 16GB", "Ultra 5 225U"},
		{"Gaming Ultra 9 285HX 32GB", "Ultra 9 285HX"},
		{"DELL Pro Max Premium 14 U7 265H 32GB 1TB RTX PRO 1000", "Ultra 7 265H"},
		{"HP Z8 8 G2i CU7-356H DT3F0ET 32GB", "Ultra 7 356H"},
		{"Lenovo Legion 7 U9-275HX 32GB 1TB RTX5070 OLED", "Ultra 9 275HX"},
		{"Hitno! Lenovo X301 Intel U9400/4gbddr2/13,3 Led slim", ""},
		{"Laptop i5- 6200. 8gb ddr 256ssd", ""},
		{"GETAC X500 G3 XQ2SZ5WDTDXL Black", ""},
		{"Laptop LENOVO IdeaPad 5 14ITL05 DOS/14\"IPS FHD", ""},
		{"Laptopovi odmah spremni za rad +GARANCIJA 12 meseci Novi Sad", ""},
	}
	for _, c := range cases {
		if got := ExtractCPU(c.text); got != c.want {
			t.Errorf("ExtractCPU(%q) = %q, хочу %q", c.text, got, c.want)
		}
	}
}

func TestExtractLaptopModel(t *testing.T) {
	cases := []struct{ text, want string }{
		{"Lenovo ThinkPad E14 Gen 6 – Ryzen 7 / 16GB / 512GB", "Lenovo ThinkPad E14 Gen 6"},
		{"The Lenovo ThinkPad E14 Gen 6 (21M3003PCX) is a 14-inch laptop", "Lenovo ThinkPad E14 Gen 6 21M3003PCX"},
		{"LENOVO ThinkPad E14 G6 Ryzen 7 7735HS 16GB 512GB", "Lenovo ThinkPad E14 Gen 6"},
		{"Lenovo IdeaPad Ryzen 7 / 16GB / 512GB", ""},
	}
	for _, c := range cases {
		if got := ExtractLaptopModel(c.text); got != c.want {
			t.Errorf("ExtractLaptopModel(%q) = %q, хочу %q", c.text, got, c.want)
		}
	}
}

func TestExtractExactModelCode(t *testing.T) {
	if got := ExtractExactModelCode("Lenovo ThinkPad E14 Gen 6 21M3003PCX"); got != "21M3003PCX" {
		t.Fatalf("ExtractExactModelCode = %q, want 21M3003PCX", got)
	}
	if got := ExtractExactModelCode("Lenovo ThinkPad E14 Gen 6"); got != "" {
		t.Fatalf("family-only model must not be exact code, got %q", got)
	}
}

func TestLookupExactModelSpecs(t *testing.T) {
	gs, ok := LookupExactModelSpecs("Lenovo ThinkPad E14 Gen 6 (21M3003PCX)")
	if !ok {
		t.Fatal("expected exact MTM catalog match")
	}
	if gs.LaptopModel != "Lenovo ThinkPad E14 Gen 6 21M3003PCX" ||
		gs.CPU != "Ryzen 7 7735HS" || gs.RAMGB != 16 || gs.SSDGB != 512 || !gs.GPUIntegrated() {
		t.Fatalf("catalog specs = %+v", gs)
	}
	if _, ok := LookupExactModelSpecs("Lenovo ThinkPad E14 Gen 6 Ryzen 7 16GB 512GB"); ok {
		t.Fatal("family-only model must not match exact catalog")
	}
}

func TestExtractGPU(t *testing.T) {
	cases := []struct{ text, want string }{
		{"Acer Predator Helios Neo 16 i7-13700HX/32GB DDR5/1TB/RTX4060", "RTX 4060"},
		{"Gaming laptop GTX 1650 Ti 16GB", "GTX 1650 TI"},
		{"Lenovo Legion 5 Ryzen 5 4600H RX 6600M", "RX 6600M"},
		{"Dell 7550 i7-10750H/32GB/512GB NVMe/T2000 4GB", "T2000"},
		{"HP EliteBook 840 G6 i5-8250U", ""},
		// PLAN_v5, Фаза C: рабочие/профессиональные GPU.
		{"Lenovo P16 gen1/i7 12850HX/32gb/1tb/Nvidia RTX A3000 12gb/2k", "RTX A3000"},
		{"Dell Precision 5550 i7-10750H 16GB Quadro T2000", "Quadro T2000"},
		{"HP ZBook Studio G5 i7-8850H Quadro P2000", "Quadro P2000"},
		{"Apple MacBook Pro 16 i9 Radeon Pro 5500M", "Radeon Pro 5500M"},
		{"Asus Zenbook Pro 14 i7-12700H Arc A370M", "Arc A370M"},
		{"Lenovo P16v Ultra 7 165H 64gb ddr5 1Tb nvm rtx 500 ada 4gb", "RTX 500 Ada"},
		{"DELL Pro Max Premium 14 U7 265H 32GB 1TB RTX PRO 1000 8GB", "RTX PRO 1000"},
		{"HP ZBook Fury G1i 16 Ultra 9 RTX PRO 4000 Blackwell", "RTX PRO 4000"},
		{"Lenovo Thinkpad P52/15.6 IPS/I7-8850H/32/512/P2000 4GB", "Quadro P2000"},
		{"Dell Precision 7520 - i7-6820HQ/32Gb/480Gb/M1200 4Gb/FHD", "Quadro M1200"},
		{"Dell M6800 i7-4810QM/32gbddr3/K4100 4GB DDR5", "Quadro K4100M"},
		{"Laptop Lenovo ThinkPad P15 Gen2 i7 11th/32GB/1TB/A2000 4GB", "RTX A2000"},
		{"Lenovo ThinkPad T490 i5-8265U 8GB", ""},       // T490 — модель ноутбука, не GPU
		{"Lenovo ThinkPad T14 Gen 2 16GB", ""},          // T14 — не GPU
		{"Lenovo ThinkPad P52 i7-8850H 32GB 512GB", ""}, // P52 — модель ноутбука, не GPU
	}
	for _, c := range cases {
		if got := ExtractGPU(c.text); got != c.want {
			t.Errorf("ExtractGPU(%q) = %q, хочу %q", c.text, got, c.want)
		}
	}
}

func TestExtractMemory(t *testing.T) {
	cases := []struct {
		text     string
		ram, ssd int
	}{
		{"Lenovo ThinkPad T490 i7-8665U 16GB DDR4 512GB SSD nvme", 16, 512},
		{"Lenovo ThinkPad 15.6 i5-8265u 16gb 512g ssd #29", 16, 512},
		{"Dell 7550 i7-10750H/32GB/512GB NVMe/T2000 4GB/15.6\" FHD", 32, 512},
		{"Lenovo P16 gen1/i7 12850HX/32gb/1tb/Nvidia RTX A3000 12gb/2k", 32, 1024},
		{"HP EliteBook 630 G9 i5-1235U 16GB 256GB SSD +GARANCIJA", 16, 256},
		{"DELL Alienware 16X Aurora – Ultra 9 / 32GB RAM", 32, 0},
		{"Radni laptop i5/16gb/SSD +GARANCIJA", 16, 0},
		{"Lenovo ThinkPad E14 Gen 6 – Ryzen 7 / 16GB / 512GB", 16, 512},
		{"Lenovo ThinkPad P52 Intel i7-8850H Quadro P1000 4GB 16GB 512", 16, 512},
		{"Laptop MSI GE75 Raider 8RF / i7-8750H / GTX 1070 8GB / 32GB", 32, 0},
		{"Dell i5 12500H / RTX 3050 Ti / 16GB DDR5 / 512GB nvme", 16, 512},
		{"ASUS ROG Zephyrus G14 RTX 5060 8GB 32GB LPDDR5X 1TB", 32, 1024},
		{"ThinkPad T14 Gen 2 16GB DDR4 3200MHz Iris Xe", 16, 0},
		{"SSD 512GB nov, laptop", 0, 512},
		{"Laptop 15.6\" FHD", 0, 0},
	}
	for _, c := range cases {
		ram, ssd := ExtractMemory(c.text)
		if ram != c.ram || ssd != c.ssd {
			t.Errorf("ExtractMemory(%q) = (%d, %d), хочу (%d, %d)", c.text, ram, ssd, c.ram, c.ssd)
		}
	}
}

// ---------- Gemini-спеки (расширенный формат) ----------

func TestParseGeminiSpecs_Full(t *testing.T) {
	raw := `{"laptop_model": "HP EliteBook 840 G6", "cpu": "i5-8250U", "ram_gb": 8, "ssd_gb": 256, "gpu": "integrated", "why_no_cpu": ""}`
	gs, err := ParseGeminiSpecs(raw)
	if err != nil {
		t.Fatalf("парсинг: %v", err)
	}
	if gs.LaptopModel != "HP EliteBook 840 G6" || gs.CPU != "i5-8250U" || gs.RAMGB != 8 || gs.SSDGB != 256 {
		t.Errorf("поля: %+v", gs)
	}
	if !gs.GPUIntegrated() {
		t.Errorf("gpu=integrated не распознан: %q", gs.GPU)
	}
}

func TestParseGeminiSpecs_WhyNoCPU(t *testing.T) {
	raw := "```json\n{\"laptop_model\": \"\", \"cpu\": \"\", \"ram_gb\": 8, \"ssd_gb\": 250, \"gpu\": \"\", \"why_no_cpu\": \"нет зацепок для определения модели\"}\n```"
	gs, err := ParseGeminiSpecs(raw)
	if err != nil {
		t.Fatalf("парсинг: %v", err)
	}
	if gs.CPU != "" {
		t.Errorf("cpu должен быть пуст: %q", gs.CPU)
	}
	if gs.WhyNoCPU == "" {
		t.Error("why_no_cpu не сохранён")
	}
	if gs.GPUIntegrated() {
		t.Error("пустой gpu не должен считаться встроенным")
	}
}

func TestGPUIntegrated_Variants(t *testing.T) {
	for _, v := range []string{
		"integrated", "integrated graphics", "integrisana grafika",
		"встроенная", "Встроенная видеокарта", "встройка",
		"AMD Radeon 680M Graphics", "Intel Iris Xe", "Intel UHD Graphics",
	} {
		gs := GeminiSpecs{GPU: v}
		if !gs.GPUIntegrated() {
			t.Errorf("%q должен считаться встроенным", v)
		}
	}
	for _, v := range []string{"", "RTX 3060", "RX 6600M"} {
		gs := GeminiSpecs{GPU: v}
		if gs.GPUIntegrated() {
			t.Errorf("%q не должен считаться встроенным", v)
		}
	}
}
