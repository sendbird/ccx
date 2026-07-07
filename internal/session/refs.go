package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// RefKind classifies a tracked external reference found in a session.
type RefKind string

const (
	RefPR   RefKind = "pr"
	RefJira RefKind = "jira"
)

// RefState is the resolved lifecycle state of a reference. Empty string means
// "unknown / not yet resolved" — the URL is known but its status could not be
// (or has not yet been) fetched.
type RefState string

const (
	RefStateUnknown RefState = ""
	// PR states (from gh: OPEN/CLOSED/MERGED, plus a synthetic DRAFT).
	RefStateOpen   RefState = "OPEN"
	RefStateDraft  RefState = "DRAFT"
	RefStateMerged RefState = "MERGED"
	RefStateClosed RefState = "CLOSED"
	// Jira states are free-form (project-defined), so we keep the raw category
	// name and expose a coarse Done flag separately.
)

// SessionRef is one PR or Jira reference discovered in a session, plus whatever
// status we could resolve for it.
type SessionRef struct {
	Kind  RefKind
	URL   string
	Label string // e.g. "sendbird/ccx#52" or "CPLAT-1234"

	State RefState // resolved lifecycle state (RefStateUnknown until fetched)

	// PR-only detail.
	ReviewDecision string // "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED", ""
	ChecksState    string // "SUCCESS", "FAILURE", "PENDING", "" (rolled up)
	IsDraft        bool

	// Jira-only detail.
	JiraStatus     string // raw status name, e.g. "In Progress"
	JiraStatusDone bool   // status category is "done"

	Resolved  bool // true once a status fetch completed (even if it failed)
	FetchedAt time.Time
}

// IsOpen reports whether this reference represents active/unfinished work worth
// surfacing prominently (open/draft PR, or a not-done Jira issue).
func (r SessionRef) IsOpen() bool {
	switch r.Kind {
	case RefPR:
		return r.State == RefStateOpen || r.State == RefStateDraft
	case RefJira:
		// Unknown Jira state is treated as open-ish so the link still surfaces;
		// a resolved "done" is the only thing we hide from the open count.
		return !(r.Resolved && r.JiraStatusDone)
	}
	return false
}

// LoadSessionRefs reads a session's JSONL, extracts PR/Jira references, and
// resolves their status (network-bound, TTL-cached). Returns nil when the file
// has no such references. Intended for lazy enrichment when a session preview
// or badge needs ref detail.
func LoadSessionRefs(ctx context.Context, filePath string) []SessionRef {
	entries, err := LoadMessages(filePath)
	if err != nil {
		return nil
	}
	refs := ExtractSessionRefs(entries)
	if len(refs) == 0 {
		return nil
	}
	return ResolveRefs(ctx, refs)
}

// ExtractSessionRefs scans a session's entries for PR and Jira URLs and returns
// a deduplicated, ordered list (PRs first, then Jira). Status is NOT resolved
// here — callers use ResolveRefs for that (it is network-bound and cached).
func ExtractSessionRefs(entries []Entry) []SessionRef {
	seen := make(map[string]bool)
	var refs []SessionRef
	for i := range entries {
		for _, b := range entries[i].Content {
			for _, text := range [2]string{b.Text, b.ToolInput} {
				if text == "" {
					continue
				}
				for _, raw := range refURLRegex.FindAllString(text, -1) {
					u := cleanRefURL(raw)
					if u == "" || seen[u] {
						continue
					}
					ref, ok := classifyRef(u)
					if !ok {
						continue
					}
					seen[u] = true
					refs = append(refs, ref)
				}
			}
		}
	}
	sortRefs(refs)
	return refs
}

func sortRefs(refs []SessionRef) {
	order := map[RefKind]int{RefPR: 0, RefJira: 1}
	sort.SliceStable(refs, func(i, j int) bool {
		return order[refs[i].Kind] < order[refs[j].Kind]
	})
}

// ---- URL classification (kept local so the session package has no dependency
// on internal/extract, which itself depends on this package).

var refURLRegex = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `\)\]\\]+`)

// jiraKeyRegex matches a canonical Jira issue key: uppercase project + number.
// Used to trim trailing prose glued onto a browse URL (e.g. "CPLAT-10753Follow-up").
var jiraKeyRegex = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+`)

// prNumRegex matches the leading digits of a PR number segment, trimming any
// prose glued onto it (e.g. "435CPLAT-10747" → "435").
var prNumRegex = regexp.MustCompile(`^[0-9]+`)

// classifyRef turns a raw URL into a SessionRef if it is a GitHub PR or a Jira
// browse link. Returns ok=false otherwise.
//
// A GitHub "/pull/new/<branch>" URL is the compare/create page, not an existing
// PR — `gh pr view` can never resolve it, so it would sit at unknown status
// forever. We only accept "/pull/<number>" where <number> starts with a digit.
func classifyRef(u string) (SessionRef, bool) {
	low := strings.ToLower(u)
	switch {
	case strings.Contains(low, "github.com") && strings.Contains(low, "/pull/"):
		num := prNumber(u)
		if num == "" {
			return SessionRef{}, false // compare page or malformed → not a real PR
		}
		return SessionRef{Kind: RefPR, URL: u, Label: prLabel(u, num)}, true
	case strings.Contains(low, "atlassian.net") && strings.Contains(low, "/browse/"):
		return SessionRef{Kind: RefJira, URL: u, Label: jiraKey(u)}, true
	}
	return SessionRef{}, false
}

// prNumber returns the leading digits of a PR URL's number segment, or "" if the
// segment is not numeric (e.g. "new" in a "/pull/new/<branch>" compare URL).
func prNumber(u string) string {
	parts := strings.Split(strings.Trim(pathOf(u), "/"), "/")
	if len(parts) >= 4 && parts[2] == "pull" {
		return prNumRegex.FindString(trimQuery(parts[3]))
	}
	return ""
}

// prLabel builds "owner/repo#number" from a PR URL. The already-validated
// leading-digit number segment is passed in by classifyRef.
func prLabel(u, num string) string {
	parts := strings.Split(strings.Trim(pathOf(u), "/"), "/")
	if len(parts) >= 4 && parts[2] == "pull" && num != "" {
		return parts[0] + "/" + parts[1] + "#" + num
	}
	return u
}

// jiraKey extracts the issue key (e.g. CPLAT-1234) from a browse URL. Trailing
// prose glued onto the segment (e.g. "CPLAT-10753Follow-up") is trimmed to the
// canonical key.
func jiraKey(u string) string {
	p := pathOf(u)
	idx := strings.Index(p, "/browse/")
	if idx < 0 {
		return u
	}
	seg := trimQuery(strings.Trim(p[idx+len("/browse/"):], "/"))
	if m := jiraKeyRegex.FindString(seg); m != "" {
		return m
	}
	return seg
}

// ---- Status resolution.

// refCacheEntry is a resolved ref plus a timestamp for TTL expiry.
type refCacheEntry struct {
	ref SessionRef
	at  time.Time
}

var (
	refCacheMu  sync.Mutex
	refCache    = map[string]refCacheEntry{}
	refCacheTTL = 5 * time.Minute
)

// jiraAuth holds resolved Jira REST credentials, probed once.
var (
	jiraAuthOnce  sync.Once
	jiraBaseURL   string
	jiraEmail     string
	jiraToken     string
	jiraAvailable bool
)

func initJiraAuth() {
	jiraAuthOnce.Do(func() {
		jiraToken = firstNonEmpty(os.Getenv("JIRA_API_TOKEN"), os.Getenv("JIRA_API_KEY"), os.Getenv("ATLASSIAN_TOKEN"))
		jiraEmail = firstNonEmpty(os.Getenv("JIRA_EMAIL"), os.Getenv("JIRA_LOGIN"))
		jiraBaseURL = strings.TrimRight(firstNonEmpty(os.Getenv("JIRA_BASE_URL"), "https://sendbird.atlassian.net"), "/")
		jiraAvailable = jiraToken != "" && jiraEmail != ""
	})
}

// ResolveRefs fetches and fills status for each ref, using a process-wide TTL
// cache keyed by URL. Network calls are bounded by ctx. Failures are recorded
// as Resolved=true with State=Unknown so callers can render "link only" and we
// don't hammer a broken endpoint every refresh.
func ResolveRefs(ctx context.Context, refs []SessionRef) []SessionRef {
	out := make([]SessionRef, len(refs))
	for i, r := range refs {
		if cached, ok := getCachedRef(r.URL); ok {
			out[i] = cached
			continue
		}
		resolved := resolveOne(ctx, r)
		resolved.Resolved = true
		resolved.FetchedAt = time.Now()
		setCachedRef(resolved)
		out[i] = resolved
	}
	return out
}

func getCachedRef(url string) (SessionRef, bool) {
	refCacheMu.Lock()
	defer refCacheMu.Unlock()
	e, ok := refCache[url]
	if !ok || time.Since(e.at) > refCacheTTL {
		return SessionRef{}, false
	}
	return e.ref, true
}

func setCachedRef(r SessionRef) {
	refCacheMu.Lock()
	defer refCacheMu.Unlock()
	refCache[r.URL] = refCacheEntry{ref: r, at: time.Now()}
}

func resolveOne(ctx context.Context, r SessionRef) SessionRef {
	switch r.Kind {
	case RefPR:
		return resolvePR(ctx, r)
	case RefJira:
		return resolveJira(ctx, r)
	}
	return r
}

// ghPRView mirrors the JSON we request from `gh pr view`.
type ghPRView struct {
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	ReviewDecision    string `json:"reviewDecision"`
	StatusCheckRollup []struct {
		State      string `json:"state"`      // check runs: SUCCESS/FAILURE/...
		Conclusion string `json:"conclusion"` // sometimes empty
		Status     string `json:"status"`     // COMPLETED/IN_PROGRESS/...
	} `json:"statusCheckRollup"`
}

func resolvePR(ctx context.Context, r SessionRef) SessionRef {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", r.URL,
		"--json", "state,isDraft,reviewDecision,statusCheckRollup")
	out, err := cmd.Output()
	if err != nil {
		return r // leave State=Unknown; link still renders
	}
	var v ghPRView
	if err := json.Unmarshal(out, &v); err != nil {
		return r
	}
	r.IsDraft = v.IsDraft
	r.ReviewDecision = v.ReviewDecision
	switch strings.ToUpper(v.State) {
	case "OPEN":
		if v.IsDraft {
			r.State = RefStateDraft
		} else {
			r.State = RefStateOpen
		}
	case "MERGED":
		r.State = RefStateMerged
	case "CLOSED":
		r.State = RefStateClosed
	}
	r.ChecksState = rollupChecks(v)
	return r
}

// rollupChecks reduces the per-check rollup to a single coarse state.
func rollupChecks(v ghPRView) string {
	if len(v.StatusCheckRollup) == 0 {
		return ""
	}
	anyPending, anyFail := false, false
	for _, c := range v.StatusCheckRollup {
		concl := strings.ToUpper(firstNonEmpty(c.Conclusion, c.State))
		st := strings.ToUpper(c.Status)
		switch {
		case concl == "FAILURE" || concl == "ERROR" || concl == "CANCELLED" || concl == "TIMED_OUT":
			anyFail = true
		case st != "" && st != "COMPLETED":
			anyPending = true
		case concl == "" && st == "":
			anyPending = true
		}
	}
	switch {
	case anyFail:
		return "FAILURE"
	case anyPending:
		return "PENDING"
	default:
		return "SUCCESS"
	}
}

// jiraIssueView mirrors the fields we request from the Jira REST API.
type jiraIssueView struct {
	Fields struct {
		Status struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"` // "new", "indeterminate", "done"
			} `json:"statusCategory"`
		} `json:"status"`
	} `json:"fields"`
}

func resolveJira(ctx context.Context, r SessionRef) SessionRef {
	initJiraAuth()
	if !jiraAvailable {
		return r // no creds → link only
	}
	key := r.Label
	if key == "" {
		return r
	}
	url := jiraBaseURL + "/rest/api/3/issue/" + key + "?fields=status"
	req, err := httpNewRequest(ctx, "GET", url)
	if err != nil {
		return r
	}
	req.SetBasicAuth(jiraEmail, jiraToken)
	req.Header.Set("Accept", "application/json")
	body, ok := httpDoJSON(req)
	if !ok {
		return r
	}
	var v jiraIssueView
	if err := json.Unmarshal(body, &v); err != nil {
		return r
	}
	r.JiraStatus = v.Fields.Status.Name
	r.JiraStatusDone = strings.EqualFold(v.Fields.Status.StatusCategory.Key, "done")
	if r.JiraStatusDone {
		r.State = RefStateMerged // reuse "done-like" coloring downstream
	} else if r.JiraStatus != "" {
		r.State = RefStateOpen
	}
	return r
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- small URL/HTTP helpers ------------------------------------------------

// cleanRefURL strips JSON-escape artifacts and trailing punctuation, then
// validates the URL. Returns "" if it is not a usable absolute URL.
//
// A literal escape sequence (backslash-n, -t, -r) or a literal backslash marks
// the end of the URL: transcripts store text as raw JSON, so two URLs separated
// by "\n" arrive glued as `...pull/431\nhttps://...`. We cut at the first such
// marker instead of deleting it, otherwise the two URLs concatenate into one.
func cleanRefURL(raw string) string {
	// JSON may escape forward slashes as "\/"; restore them first.
	raw = strings.ReplaceAll(raw, `\/`, "/")
	// Cut at the first remaining literal escape marker or backslash.
	if i := strings.IndexByte(raw, '\\'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimRightFunc(raw, func(r rune) bool {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return false
		}
		switch r {
		case '-', '_', '~', '/', '=', '%', '?', '&', '.':
			return false
		}
		return true
	})
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return raw
}

// pathOf returns the path component of a URL (empty on parse failure).
func pathOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Path
}

// trimQuery removes any residual query/fragment from a path segment.
func trimQuery(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

func httpNewRequest(ctx context.Context, method, url string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, nil)
}

var refHTTPClient = &http.Client{Timeout: 8 * time.Second}

// httpDoJSON performs the request and returns the body if the status is 2xx.
func httpDoJSON(req *http.Request) ([]byte, bool) {
	resp, err := refHTTPClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return body, true
}

// OpenRefCounts returns how many of the session's resolved refs are open PRs
// and open (non-done) Jira issues. Only meaningful once Refs is populated.
func (s Session) OpenRefCounts() (openPRs, openJira int) {
	for _, r := range s.Refs {
		if !r.IsOpen() {
			continue
		}
		switch r.Kind {
		case RefPR:
			openPRs++
		case RefJira:
			openJira++
		}
	}
	return openPRs, openJira
}
