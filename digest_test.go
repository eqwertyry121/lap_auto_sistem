package main

import (
	"strings"
	"testing"
	"time"

	"kpbot/models"
)

func TestParseDigestTime(t *testing.T) {
	cases := []struct {
		in      string
		h, m    int
		wantErr bool
	}{
		{"09:00", 9, 0, false},
		{"23:59", 23, 59, false},
		{"00:00", 0, 0, false},
		{"24:00", 0, 0, true},
		{"12:60", 0, 0, true},
		{"abc", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, c := range cases {
		h, m, err := parseDigestTime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: ждал ошибку", c.in)
			}
			continue
		}
		if err != nil || h != c.h || m != c.m {
			t.Errorf("%q: got (%d,%d,%v), want (%d,%d)", c.in, h, m, err, c.h, c.m)
		}
	}
}

func TestNextDigestTime(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 8, 4, 10, 0, 0, 0, loc)

	// 09:00 уже прошло сегодня → завтра 09:00.
	got := nextDigestTime(now, 9, 0)
	want := time.Date(2026, 8, 5, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("прошлое время: got %s, want %s", got, want)
	}

	// 15:30 ещё впереди → сегодня.
	got = nextDigestTime(now, 15, 30)
	want = time.Date(2026, 8, 4, 15, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("будущее время: got %s, want %s", got, want)
	}

	// Ровно текущий момент — уже «не будущее», перенос на завтра.
	got = nextDigestTime(now, 10, 0)
	want = time.Date(2026, 8, 5, 10, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("равное время: got %s, want %s", got, want)
	}
}

func TestDigestText(t *testing.T) {
	byStatus := map[string]int{"ALERTED": 2, "NO_DEAL": 10, "SKIPPED_SPAM": 3}
	alerts := []models.Listing{{
		Title:    strings.Repeat("x", 100),
		Price:    240,
		Currency: "EUR",
	}}
	text := digestText(25*time.Hour+13*time.Minute, byStatus, 4, 24677, 858, alerts)

	for _, want := range []string{
		"Лоты за 24ч:</b> 15",
		"ALERTED: 2",
		"Челленджи KP (с запуска):</b> 4",
		"858/24677",
		"3.5%",
		"€240",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("дайджест не содержит %q:\n%s", want, text)
		}
	}
	// Длинный заголовок обрезается.
	if strings.Count(text, "x") != 59 {
		t.Errorf("заголовок алерта не обрезан до 60 символов")
	}
}
