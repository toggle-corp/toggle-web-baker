package view

import (
	"testing"
	"time"
)

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{3435973836, "3.2 GiB"},
		{1099511627776, "1.0 TiB"},
		{-1024, "-1.0 KiB"},
		{-512, "-512 B"},
	}
	for _, c := range cases {
		if got := HumanizeBytes(c.n); got != c.want {
			t.Errorf("HumanizeBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestHumanizeDelta(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "▬ no change"},
		{125829120, "▲ +120.0 MiB"},
		{-1288490188, "▼ -1.2 GiB"},
	}
	for _, c := range cases {
		if got := HumanizeDelta(c.n); got != c.want {
			t.Errorf("HumanizeDelta(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	fixed := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	old := Now
	Now = func() time.Time { return fixed }
	defer func() { Now = old }()

	cases := []struct {
		ts      string
		wantRel string
		wantAbs string
	}{
		{"2026-06-30T11:58:00Z", "2m ago", "2026-06-30T11:58:00Z"},
		{"2026-06-30T11:59:30Z", "just now", "2026-06-30T11:59:30Z"},
		{"2026-06-30T12:00:00Z", "just now", "2026-06-30T12:00:00Z"},
		{"2026-06-30T12:05:00Z", "in 5m", "2026-06-30T12:05:00Z"},
		{"", "—", ""},
		{"not-a-time", "—", ""},
	}
	for _, c := range cases {
		rel, abs := RelativeTime(c.ts)
		if rel != c.wantRel || abs != c.wantAbs {
			t.Errorf("RelativeTime(%q) = (%q,%q), want (%q,%q)", c.ts, rel, abs, c.wantRel, c.wantAbs)
		}
	}
}
