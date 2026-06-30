package k8s

import "testing"

func TestParseUsage(t *testing.T) {
	cases := []struct {
		name      string
		cpu       string
		mem       string
		wantMilli int64
		wantBytes int64
	}{
		{"millicores + binary mem", "1500m", "2Gi", 1500, 2147483648},
		{"whole cores + decimal bytes", "1", "2143289344", 1000, 2143289344},
		{"sub-core + mebibytes", "350m", "2096Mi", 350, 2096 * 1024 * 1024},
		{"empty strings -> zero", "", "", 0, 0},
		{"garbage -> zero", "not-a-quantity", "also-bad", 0, 0},
	}
	for _, c := range cases {
		gotMilli, gotBytes := parseUsage(c.cpu, c.mem)
		if gotMilli != c.wantMilli || gotBytes != c.wantBytes {
			t.Errorf("%s: parseUsage(%q,%q) = (%d,%d), want (%d,%d)",
				c.name, c.cpu, c.mem, gotMilli, gotBytes, c.wantMilli, c.wantBytes)
		}
	}
}
