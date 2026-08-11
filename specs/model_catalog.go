package specs

import "strings"

var exactModelSpecs = map[string]GeminiSpecs{
	// Lenovo PSREF: ThinkPad E14 Gen 6 (AMD), MTM 21M3003PCX.
	"21M3003PCX": {
		LaptopModel: "Lenovo ThinkPad E14 Gen 6 21M3003PCX",
		CPU:         "Ryzen 7 7735HS",
		RAMGB:       16,
		SSDGB:       512,
		GPU:         "integrated",
	},
}

// LookupExactModelSpecs returns specs only for exact manufacturer model codes.
// Family names such as "ThinkPad E14 Gen 6" intentionally do not match here.
func LookupExactModelSpecs(text string) (GeminiSpecs, bool) {
	code := ExtractExactModelCode(text)
	if code == "" {
		return GeminiSpecs{}, false
	}
	gs, ok := exactModelSpecs[strings.ToUpper(code)]
	return gs, ok
}
