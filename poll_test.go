package main

import (
	"testing"

	"kpbot/models"
)

func TestShouldFetchNextLiveSearchPage(t *testing.T) {
	withAds := []models.SearchAd{{AdID: 1}}
	cases := []struct {
		name     string
		res      *models.SearchResults
		page     int
		maxPages int
		want     bool
	}{
		{"nil result", nil, 1, 2, false},
		{"next page allowed", &models.SearchResults{Ads: withAds, Pages: 3}, 1, 2, true},
		{"configured limit reached", &models.SearchResults{Ads: withAds, Pages: 3}, 2, 2, false},
		{"kp pages reached", &models.SearchResults{Ads: withAds, Pages: 1}, 1, 5, false},
		{"empty page", &models.SearchResults{Ads: nil, Pages: 3}, 1, 5, false},
		{"kp max flag", &models.SearchResults{Ads: withAds, Pages: 3, HasReachedMax: true}, 1, 5, false},
		{"kp limit flag", &models.SearchResults{Ads: withAds, Pages: 3, HasReachedLimit: true}, 1, 5, false},
		{"bad config", &models.SearchResults{Ads: withAds, Pages: 3}, 1, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldFetchNextLiveSearchPage(c.res, c.page, c.maxPages); got != c.want {
				t.Fatalf("shouldFetchNextLiveSearchPage = %v, want %v", got, c.want)
			}
		})
	}
}
