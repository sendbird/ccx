package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractSessionRefsFromFile_ArtifactPublishOnly verifies that an artifact
// URL counts as a ref only when this session published it (the "Published … at
// <url>" tool_result marker). A URL merely quoted in assistant text — e.g. a
// link to another session's artifact — must NOT become a ref.
func TestExtractSessionRefsFromFile_ArtifactPublishOnly(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	lines := []string{
		// Quoted mention only — not published here. Must be skipped.
		`{"type":"assistant","timestamp":"2026-07-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"see https://claude.ai/code/artifact/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee for the other session's view"}]}}`,
		// Published here via an Artifact tool_result.
		`{"type":"user","timestamp":"2026-07-01T10:00:12.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Published /tmp/x.html at https://claude.ai/code/artifact/11111111-2222-3333-4444-555555555555"}]}}`,
	}
	if err := os.WriteFile(fp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs := ExtractSessionRefsFromFile(fp)
	if len(refs) != 1 {
		t.Fatalf("want 1 artifact ref (published only), got %d: %+v", len(refs), refs)
	}
	if refs[0].Kind != RefArtifact || refs[0].Label != "artifact:11111111" {
		t.Errorf("ref = %+v, want published artifact:11111111", refs[0])
	}
}

// TestBuildSessionFlow_ArtifactRefSkipsTextMention verifies the flow does not
// emit an ArtifactRef for an artifact URL that only appears in assistant text,
// only for one published via an Artifact tool_use + tool_result pair.
func TestBuildSessionFlow_ArtifactRefSkipsTextMention(t *testing.T) {
	root := t.TempDir()
	sessFile := filepath.Join(root, "s.jsonl")
	body := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"check this"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"text","text":"other session made https://claude.ai/code/artifact/99999999-8888-7777-6666-555555555555"}]}}
`
	if err := os.WriteFile(sessFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "s", FilePath: sessFile, ProjectPath: root, ProjectName: "s"}
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}
	refs := fi.Artifacts(fi.RootID, ArtifactRef, ScopeSession)
	for _, a := range refs {
		if ref, ok := a.Data.(SessionRef); ok && ref.Kind == RefArtifact {
			t.Errorf("text-only artifact mention emitted as ref: %+v", ref)
		}
	}
	// Facets should not count a text-only artifact mention as a ref.
	facets := fi.Facets(fi.RootID, ScopeSession)
	if facets.Counts[ArtifactRef] != 0 {
		t.Errorf("facet ArtifactRef count = %d, want 0 for text-only mention", facets.Counts[ArtifactRef])
	}
}
