package controller

import (
	"strings"
	"testing"
)

func validConfig() OperatorConfig {
	c := OperatorConfig{
		ClusterCIDRs: []string{"10.0.0.0/8", "172.20.0.0/16"},
		TraefikGroup: "traefik.io",
	}
	return c
}

// Fix 4: a malformed cluster CIDR must be rejected by Validate.
func TestConfigValidate_RejectsMalformedClusterCIDR(t *testing.T) {
	c := validConfig()
	c.ClusterCIDRs = []string{"10.0.0.0/8", "not-a-cidr"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for malformed CIDR, got nil")
	}
	if !strings.Contains(err.Error(), "not-a-cidr") {
		t.Fatalf("expected error to name the bad CIDR, got %v", err)
	}
}

// Fix 4: a CIDR without a mask (host IP) must also be rejected (ParseCIDR fails).
func TestConfigValidate_RejectsBareIP(t *testing.T) {
	c := validConfig()
	c.ClusterCIDRs = []string{"10.0.0.1"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for bare IP without mask, got nil")
	}
}

// Fix 4: valid CIDRs pass.
func TestConfigValidate_AcceptsValidCIDRs(t *testing.T) {
	c := validConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

// Fix 4: empty cluster CIDRs still rejected (refuse-if-unset preserved).
func TestConfigValidate_RejectsEmptyClusterCIDRs(t *testing.T) {
	c := validConfig()
	c.ClusterCIDRs = nil
	if err := c.Validate(); err == nil {
		t.Fatal("expected error when cluster CIDRs unset, got nil")
	}
}
