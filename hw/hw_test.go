package hw

import "testing"

func TestMatchGPUAllowsMobileSuffix(t *testing.T) {
	gpus := map[string]GPU{
		Key("NVIDIA Quadro P1000 Mobile"): {Name: "NVIDIA Quadro P1000 Mobile", Score: 8693},
	}
	got, ok := MatchGPU(gpus, "Quadro P1000")
	if !ok {
		t.Fatal("expected Quadro P1000 to match mobile catalog entry")
	}
	if got.Name != "NVIDIA Quadro P1000 Mobile" || got.Score != 8693 {
		t.Fatalf("MatchGPU = %+v", got)
	}
}

func TestMatchGPUDoesNotMatchStrongerModifier(t *testing.T) {
	gpus := map[string]GPU{
		Key("NVIDIA GeForce RTX 3060 Ti"):     {Name: "NVIDIA GeForce RTX 3060 Ti", Score: 52000},
		Key("NVIDIA GeForce RTX 3060 Mobile"): {Name: "NVIDIA GeForce RTX 3060 Mobile", Score: 40000},
	}
	got, ok := MatchGPU(gpus, "RTX 3060")
	if !ok {
		t.Fatal("expected RTX 3060 to match mobile base model")
	}
	if got.Name != "NVIDIA GeForce RTX 3060 Mobile" {
		t.Fatalf("MatchGPU = %+v, want mobile base model", got)
	}
}
