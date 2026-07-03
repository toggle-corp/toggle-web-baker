package v1alpha1

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The skip-exclusivity CEL rule on SetupPhaseSpec hand-enumerates PhaseSpec's
// fields (has(self.command) && has(self.image) && ...). Nothing else notices
// when PhaseSpec grows a field, so this guard walks PhaseSpec's json tags and
// asserts each one is covered by the generated CRD's rule — a new field that
// is combinable with skip:true must fail HERE, forcing the rule (and its
// envtest cases) to be extended deliberately.
func TestSetupSkipCELRuleCoversEveryPhaseSpecField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "baker.toggle-corp.com_apps.yaml"))
	if err != nil {
		t.Fatalf("read generated CRD: %v", err)
	}
	crd := string(raw)
	marker := "setup.skip cannot be combined"
	idx := strings.Index(crd, marker)
	if idx == -1 {
		t.Fatalf("generated CRD no longer carries the %q rule", marker)
	}
	// The rule: line precedes the message: line in the generated yaml; search a
	// window around the message for the rule text.
	window := crd[max(0, idx-2000):min(len(crd), idx+2000)]

	typ := reflect.TypeOf(PhaseSpec{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("PhaseSpec field %s has no json tag", typ.Field(i).Name)
		}
		if !strings.Contains(window, "has(self."+tag+")") {
			t.Errorf("PhaseSpec field %q is not covered by the setup.skip exclusivity CEL rule — extend the XValidation marker on SetupPhaseSpec (and the envtest cases) for the new field", tag)
		}
	}
}
