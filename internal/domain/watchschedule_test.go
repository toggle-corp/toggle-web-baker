package domain

import "testing"

func TestWatchCron(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		want     string
		wantErr  bool
	}{
		{"five minutes", "5m", "*/5 * * * *", false},
		{"ten minutes", "10m", "*/10 * * * *", false},
		{"uneven divisor is accepted", "7m", "*/7 * * * *", false},
		{"one minute floor", "1m", "*/1 * * * *", false},
		{"whole hour", "1h", "0 */1 * * *", false},
		{"multiple hours", "2h", "0 */2 * * *", false},
		{"90m rejected: >59m and not whole hours", "90m", "", true},
		{"sub-minute rejected", "30s", "", true},
		{"zero rejected", "0m", "", true},
		{"negative rejected", "-5m", "", true},
		{"garbage rejected", "often", "", true},
		{"empty rejected", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WatchCron(tt.interval)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("WatchCron(%q) = %q, want error", tt.interval, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("WatchCron(%q) unexpected error: %v", tt.interval, err)
			}
			if got != tt.want {
				t.Fatalf("WatchCron(%q) = %q, want %q", tt.interval, got, tt.want)
			}
		})
	}
}
