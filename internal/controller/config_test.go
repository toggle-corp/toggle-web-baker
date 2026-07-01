package controller

import (
	"strings"
	"testing"
)

// Platform helper images default to the flat ghcr.io/toggle-corp/toggle-web-baker-<name>
// repo scheme (matching images/Makefile), still digest-pinned. These defaults are a
// fallback for non-helm runs; the Helm chart supplies real refs via the -image-* flags.
func TestConfigDefaults_HelperImageRepos(t *testing.T) {
	c := validConfig()
	c.Defaults()
	cases := map[string]string{
		"clone":   c.Images.Clone,
		"copier":  c.Images.Copier,
		"du":      c.Images.Du,
		"cleanup": c.Images.Cleanup,
	}
	for name, ref := range cases {
		wantRepo := "ghcr.io/toggle-corp/toggle-web-baker-" + name
		if !strings.HasPrefix(ref, wantRepo+"@sha256:") {
			t.Errorf("Images.%s = %q, want prefix %q@sha256:", name, ref, wantRepo)
		}
	}
}

// The node-image map arrives as a single JSON flag (Helm-templated from
// values.yaml). Empty is valid (feature simply unused); malformed JSON is a
// hard error so a bad chart value fails loudly at startup.
func TestParseNodeImages_ParsesJSONMap(t *testing.T) {
	m, err := ParseNodeImages(`{"18":{"image":"ghcr.io/x-node18@sha256:aaa","runAsUser":1000},"24":{"image":"ghcr.io/x-node24@sha256:bbb","runAsUser":1000,"home":"/home/node"}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["18"].Image != "ghcr.io/x-node18@sha256:aaa" || m["18"].RunAsUser == nil || *m["18"].RunAsUser != 1000 {
		t.Fatalf("node18 entry parsed wrong: %+v", m["18"])
	}
	if m["24"].Home != "/home/node" {
		t.Fatalf("node24 home parsed wrong: %+v", m["24"])
	}
}

func TestParseNodeImages_EmptyIsNilNoError(t *testing.T) {
	m, err := ParseNodeImages("")
	if err != nil {
		t.Fatalf("empty must not error, got %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("empty must yield no entries, got %+v", m)
	}
}

func TestParseNodeImages_MalformedErrors(t *testing.T) {
	if _, err := ParseNodeImages(`{"18": not-json}`); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// A semantically broken entry must fail at parse (startup) time, not deep in the
// build pipeline. Empty image / missing UID / root UID / non-numeric key.
func TestParseNodeImages_RejectsBrokenEntries(t *testing.T) {
	cases := map[string]string{
		"empty image":     `{"18":{"runAsUser":1000}}`,
		"missing UID":     `{"18":{"image":"repo@sha256:aaa"}}`,
		"root UID":        `{"18":{"image":"repo@sha256:aaa","runAsUser":0}}`,
		"non-numeric key": `{"lts":{"image":"repo@sha256:aaa","runAsUser":1000}}`,
	}
	for name, in := range cases {
		if _, err := ParseNodeImages(in); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

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
