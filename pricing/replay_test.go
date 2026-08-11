package pricing

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type replayCase struct {
	Name                   string      `json:"name"`
	Target                 replayLot   `json:"target"`
	Market                 []replayLot `json:"market"`
	WantStepUpAdID         *int64      `json:"want_step_up_ad_id"`
	WantNoStepUp           bool        `json:"want_no_step_up"`
	WantDominatedByAdID    *int64      `json:"want_dominated_by_ad_id"`
	WantOpportunityCeiling *float64    `json:"want_opportunity_ceiling"`
	WantComparableMedian   *float64    `json:"want_comparable_median"`
}

type replayLot struct {
	AdID     int64   `json:"ad_id"`
	Title    string  `json:"title"`
	URL      string  `json:"url"`
	Kind     string  `json:"kind"`
	CPUModel string  `json:"cpu_model"`
	CPUScore float64 `json:"cpu_score"`
	GPUModel string  `json:"gpu_model"`
	GPUScore float64 `json:"gpu_score"`
	RAMGB    int     `json:"ram_gb"`
	SSDGB    int     `json:"ssd_gb"`
	Price    float64 `json:"price"`
	IsShop   bool    `json:"is_shop"`
}

func TestReplayMarketCases(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "replay_market_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []replayCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty replay dataset")
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var lots []Lot
			for _, in := range tc.Market {
				lots = append(lots, in.toLot())
			}
			m := marketWithDGPU(t, lots, 60)
			ev := m.Evaluate(tc.Target.toLot())

			if tc.WantStepUpAdID != nil {
				if ev.StepUp == nil {
					t.Fatalf("StepUp is nil, want ad_id=%d", *tc.WantStepUpAdID)
				}
				if ev.StepUp.AdID != *tc.WantStepUpAdID {
					t.Fatalf("StepUp ad_id=%d, want %d", ev.StepUp.AdID, *tc.WantStepUpAdID)
				}
				if ev.StepUp.URL == "" {
					t.Fatalf("StepUp ad_id=%d has empty URL", ev.StepUp.AdID)
				}
			}
			if tc.WantNoStepUp && ev.StepUp != nil {
				t.Fatalf("StepUp=%+v, want nil", ev.StepUp)
			}
			if tc.WantDominatedByAdID != nil {
				if ev.DominatedBy == nil {
					t.Fatalf("DominatedBy is nil, want ad_id=%d", *tc.WantDominatedByAdID)
				}
				if ev.DominatedBy.AdID != *tc.WantDominatedByAdID {
					t.Fatalf("DominatedBy ad_id=%d, want %d", ev.DominatedBy.AdID, *tc.WantDominatedByAdID)
				}
				if ev.DominatedBy.URL == "" {
					t.Fatalf("DominatedBy ad_id=%d has empty URL", ev.DominatedBy.AdID)
				}
			}
			if tc.WantOpportunityCeiling != nil && math.Abs(ev.OpportunityCeiling-*tc.WantOpportunityCeiling) > 0.01 {
				t.Fatalf("OpportunityCeiling=%.2f, want %.2f", ev.OpportunityCeiling, *tc.WantOpportunityCeiling)
			}
			if tc.WantComparableMedian != nil && math.Abs(ev.ComparableMedian-*tc.WantComparableMedian) > 0.01 {
				t.Fatalf("ComparableMedian=%.2f, want %.2f", ev.ComparableMedian, *tc.WantComparableMedian)
			}
		})
	}
}

func (r replayLot) toLot() Lot {
	kind := r.Kind
	if kind == "" {
		kind = "USED"
	}
	title := r.Title
	if title == "" {
		title = "replay lot"
	}
	return Lot{
		AdID:     r.AdID,
		Title:    title,
		URL:      r.URL,
		Kind:     kind,
		CPUModel: r.CPUModel,
		CPUScore: r.CPUScore,
		GPUModel: r.GPUModel,
		GPUScore: r.GPUScore,
		RAMGB:    r.RAMGB,
		SSDGB:    r.SSDGB,
		Price:    r.Price,
		IsShop:   r.IsShop,
		Posted:   time.Now(),
	}
}
