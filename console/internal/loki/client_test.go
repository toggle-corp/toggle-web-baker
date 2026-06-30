package loki

import "testing"

func TestConfigured(t *testing.T) {
	if New(Config{}).Configured() {
		t.Error("empty URL should be unconfigured")
	}
	if !New(Config{URL: "http://loki:3100"}).Configured() {
		t.Error("non-empty URL should be configured")
	}
}
