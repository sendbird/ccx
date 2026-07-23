package session

import (
	"sync"
	"testing"
)

// TestSetJiraAuthEnablesResolution verifies that credentials injected via
// SetJiraAuth (from the host app's config) make jiraAvailable true even when no
// JIRA_* env vars are set, and that empty fields still fall back to env.
func TestSetJiraAuthEnablesResolution(t *testing.T) {
	// Reset state deterministically.
	jiraAuthOnce = sync.Once{}
	jiraAvailable = false
	jiraEmail = ""
	jiraToken = ""
	jiraBaseURL = ""

	SetJiraAuth("gavin@example.com", "tok123", "https://sendbird.atlassian.net/")
	initJiraAuth()
	if !jiraAvailable {
		t.Fatalf("jiraAvailable=false after SetJiraAuth; email=%q token=%q", jiraEmail, jiraToken)
	}
	if jiraEmail != "gavin@example.com" {
		t.Errorf("jiraEmail=%q, want injected email", jiraEmail)
	}
	if jiraToken != "tok123" {
		t.Errorf("jiraToken=%q, want injected token", jiraToken)
	}
	if jiraBaseURL != "https://sendbird.atlassian.net" {
		t.Errorf("jiraBaseURL=%q, want trailing slash trimmed", jiraBaseURL)
	}
}

// TestSetJiraAuthEmptyFallsBackToEnv verifies empty injected fields fall back to
// environment variables.
func TestSetJiraAuthEmptyFallsBackToEnv(t *testing.T) {
	t.Setenv("JIRA_API_TOKEN", "envtok")
	t.Setenv("JIRA_EMAIL", "env@example.com")
	jiraAuthOnce = sync.Once{}
	jiraAvailable = false
	SetJiraAuth("", "", "") // no injected values
	initJiraAuth()
	if !jiraAvailable {
		t.Fatal("jiraAvailable=false; expected env fallback")
	}
	if jiraToken != "envtok" || jiraEmail != "env@example.com" {
		t.Errorf("env fallback failed: token=%q email=%q", jiraToken, jiraEmail)
	}
}

func TestCachedRefStatusRoundTrip(t *testing.T) {
	ClearRefCache()
	r := SessionRef{Kind: RefPR, URL: "https://example.com/pull/1", State: RefStateOpen}
	setCachedRef(r)
	got, ok := CachedRefStatus(r.URL)
	if !ok {
		t.Fatal("CachedRefStatus miss after setCachedRef")
	}
	if got.State != RefStateOpen {
		t.Errorf("cached State=%q, want OPEN", got.State)
	}
	ClearRefCache()
	if _, ok := CachedRefStatus(r.URL); ok {
		t.Error("CachedRefStatus hit after ClearRefCache")
	}
}
