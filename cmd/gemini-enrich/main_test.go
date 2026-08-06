package main

import "testing"

func TestParseSpecsJSON(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantCPU string
		wantRAM int
		wantErr bool
	}{
		{
			name:    "чистый JSON",
			raw:     `{"cpu":"i5-1135G7","ram_gb":16,"ssd_gb":512,"gpu":""}`,
			wantCPU: "i5-1135G7", wantRAM: 16,
		},
		{
			name:    "markdown-забор",
			raw:     "```json\n{\"cpu\":\"Ryzen 5 4600H\",\"ram_gb\":8,\"ssd_gb\":256,\"gpu\":\"GTX 1650\"}\n```",
			wantCPU: "Ryzen 5 4600H", wantRAM: 8,
		},
		{
			name:    "словесный мусор вокруг",
			raw:     "Вот характеристики: {\"cpu\":\"\",\"ram_gb\":0,\"ssd_gb\":0,\"gpu\":\"\"} — больше ничего не указано.",
			wantCPU: "", wantRAM: 0,
		},
		{name: "нет JSON", raw: "не могу определить", wantErr: true},
		{name: "битый JSON", raw: `{"cpu": `, wantErr: true},
	}
	for _, c := range cases {
		sp, err := parseSpecsJSON(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: ждал ошибку", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if sp.CPU != c.wantCPU || sp.RAMGB != c.wantRAM {
			t.Errorf("%s: got cpu=%q ram=%d", c.name, sp.CPU, sp.RAMGB)
		}
	}
}
