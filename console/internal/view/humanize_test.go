package view

import (
	"testing"
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
