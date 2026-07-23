package session

import (
	"os"
	"path/filepath"
	"testing"
)

// writeArtifactFlowFixture builds a minimal session whose transcript publishes
// one Claude Artifact: an Artifact tool_use paired with a tool_result that
// carries the claude.ai/code/artifact/<uuid> URL.
func writeArtifactFlowFixture(t *testing.T) *Session {
	t.Helper()
	root := t.TempDir()
	sessID := "artifact-flow"
	sessFile := filepath.Join(root, sessID+".jsonl")
	body := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"draw the architecture"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_art1","name":"Artifact","input":{"label":"cohome-architecture-v1","description":"cohome cell sandbox platform architecture explorer","favicon":"🧩","file_path":"/tmp/scratchpad/cohome-arch.html"}}]}}
{"type":"user","uuid":"r1","timestamp":"2026-07-01T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_art1","content":"Published /tmp/scratchpad/cohome-arch.html at https://claude.ai/code/artifact/d248181d-78cb-471d-9d94-56bea9242b23\n\nTo update: republish the same file path."}]}}
`
	if err := os.WriteFile(sessFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Session{ID: sessID, FilePath: sessFile, ProjectPath: root, ProjectName: "artifact-flow"}
}

func TestBuildSessionFlow_ArtifactRef(t *testing.T) {
	sess := writeArtifactFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	refs := fi.Artifacts(fi.RootID, ArtifactRef, ScopeSession)
	if len(refs) != 1 {
		t.Fatalf("artifact refs = %d, want 1: %+v", len(refs), refs)
	}
	art := refs[0]
	if art.Key != "https://claude.ai/code/artifact/d248181d-78cb-471d-9d94-56bea9242b23" {
		t.Errorf("artifact Key = %q, want the claude.ai URL", art.Key)
	}
	ref, ok := art.Data.(SessionRef)
	if !ok {
		t.Fatalf("artifact Data is not a SessionRef: %T", art.Data)
	}
	if ref.Kind != RefArtifact {
		t.Errorf("ref.Kind = %q, want RefArtifact", ref.Kind)
	}
	if ref.URL != art.Key {
		t.Errorf("ref.URL = %q, want %q", ref.URL, art.Key)
	}
	if ref.Title != "cohome-architecture-v1" {
		t.Errorf("ref.Title = %q, want the tool_use label", ref.Title)
	}
	if !ref.IsOpen() {
		t.Errorf("artifact ref should be IsOpen")
	}

	// Facet count rolls the artifact into the session-wide ref total.
	facets := fi.Facets(fi.RootID, ScopeSession)
	if facets.Counts[ArtifactRef] != 1 {
		t.Errorf("facet ArtifactRef count = %d, want 1", facets.Counts[ArtifactRef])
	}
}

// TestArtifactTitleFallback verifies title derivation when the tool_use input
// omits the explicit label.
func TestArtifactTitleFallback(t *testing.T) {
	if got := artifactTitle(artifactToolInput{description: "desc"}); got != "desc" {
		t.Errorf("description fallback = %q, want desc", got)
	}
	if got := artifactTitle(artifactToolInput{filePath: "/a/b/c.html"}); got != "c.html" {
		t.Errorf("file basename fallback = %q, want c.html", got)
	}
	if got := artifactTitle(artifactToolInput{}); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
}
