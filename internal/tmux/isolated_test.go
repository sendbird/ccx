package tmux

import (
	"encoding/json"
	"testing"
)

func TestInjectProjectTrustSetsApprovalFlags(t *testing.T) {
	input := []byte(`{"projects":{}}`)
	out := injectProjectTrust(input, "/tmp/ccx-cfgtest-123")

	var state map[string]json.RawMessage
	if err := json.Unmarshal(out, &state); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	var projects map[string]map[string]interface{}
	if err := json.Unmarshal(state["projects"], &projects); err != nil {
		t.Fatalf("unmarshal projects: %v", err)
	}
	entry, ok := projects["/tmp/ccx-cfgtest-123"]
	if !ok {
		t.Fatal("project entry not injected")
	}
	for _, key := range []string{
		"hasTrustDialogAccepted",
		"hasCompletedProjectOnboarding",
		"hasClaudeMdExternalIncludesApproved",
		"hasClaudeMdExternalIncludesWarningShown",
	} {
		v, ok := entry[key].(bool)
		if !ok || !v {
			t.Fatalf("expected %s=true, got %#v", key, entry[key])
		}
	}
}
