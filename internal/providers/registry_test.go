package providers

import (
	"strings"
	"testing"

	"aiquotaglass/internal/config"
)

// TestRegistryRouting verifies the plugin-style registry dispatches by type
// and that Types() only lists coded adapters with their field schemas.
func TestRegistryRouting(t *testing.T) {
	types := Types()
	var found *ProviderType
	for i := range types {
		if types[i].Type == "opencode-go" && types[i].Name != "" && types[i].Description != "" {
			found = &types[i]
		}
	}
	if found == nil {
		t.Fatalf("opencode-go not registered: %+v", types)
	}
	if len(found.Fields) == 0 {
		t.Fatal("opencode-go should declare a dynamic field schema")
	}
	keys := map[string]bool{}
	for _, f := range found.Fields {
		keys[f.Key] = true
		if f.Label == "" || f.Kind == "" {
			t.Fatalf("field %q missing label/kind", f.Key)
		}
	}
	if !keys["workspace"] || !keys["cookie"] {
		t.Fatalf("expected workspace+cookie fields, got %v", keys)
	}

	cfg := config.ProviderConfig{ID: "acct1", Type: "opencode-go", Workspace: "wrk_test"}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New(opencode-go) error: %v", err)
	}
	if p.ID() != "acct1" {
		t.Errorf("ID mismatch: got %q", p.ID())
	}

	if _, err := New(config.ProviderConfig{Type: "does-not-exist"}); err == nil {
		t.Fatal("expected UnknownProviderError")
	}
}

func TestUnknownProviderErrorMessage(t *testing.T) {
	err := (&UnknownProviderError{Type: "nope"}).Error()
	if !strings.Contains(err, "opencode-go") {
		t.Fatalf("error should list known types, got: %q", err)
	}
}

// TestTypesExposeWindowKeys verifies every registered provider type declares
// the quota window keys it actually emits, so the settings UI never asks for a
// threshold the provider never reports. The allowed sets are part of the
// documented provider contract.
func TestTypesExposeWindowKeys(t *testing.T) {
	want := map[string][]string{
		"opencode-go": {"5h", "weekly", "monthly"},
		"zhipu":       {"5h", "weekly"},
		"kimi":        {"5h", "weekly"},
		"minimax":     {"5h", "weekly"},
		"new-api":     {"total"},
		"sub2api":     {"total"},
		"sensenova":   {"5h"},
	}
	got := map[string][]string{}
	for _, pt := range Types() {
		got[pt.Type] = pt.WindowKeys
	}
	for k, w := range want {
		if !equalKeys(got[k], w) {
			t.Errorf("type %q: windowKeys = %v, want %v", k, got[k], w)
		}
	}
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
