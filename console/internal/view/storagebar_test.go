package view

import "testing"

func TestStorageBar(t *testing.T) {
	cases := []struct {
		name    string
		used    int64
		cap     int64
		wantPct int
		wantOvr bool
	}{
		{"half", 50, 100, 50, false},
		{"rounds", 1, 3, 33, false},
		{"full", 100, 100, 100, false},
		{"over", 150, 100, 150, true},
		{"empty used", 0, 100, 0, false},
		{"zero cap -> sentinel", 50, 0, -1, false},
		{"negative cap -> sentinel", 50, -10, -1, false},
	}
	for _, c := range cases {
		pct, over := StorageBar(c.used, c.cap)
		if pct != c.wantPct || over != c.wantOvr {
			t.Errorf("%s: StorageBar(%d,%d) = (%d,%v), want (%d,%v)", c.name, c.used, c.cap, pct, over, c.wantPct, c.wantOvr)
		}
	}
}
