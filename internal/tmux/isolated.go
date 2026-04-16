package tmux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// --- Isolated test environment ---
//
// Creates a fake HOME directory for running Claude in full isolation:
// no memories, no CLAUDE.md, no MCP servers, no marketplace discovery.
// Auth is provided via CLAUDE_CODE_OAUTH_TOKEN (bypasses macOS keychain).

// IsolatedEnv holds paths for an isolated Claude test environment.
type IsolatedEnv struct {
	HomeDir   string // fake HOME (tmpDir)
	ConfigDir string // fake HOME/.claude
}

// NewIsolatedEnv creates a temp HOME with onboarding state seeded
// and an empty MCP config to block all MCP servers.
func NewIsolatedEnv(prefix string) (*IsolatedEnv, error) {
	tmpDir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	configDir := filepath.Join(tmpDir, ".claude")
	os.MkdirAll(configDir, 0o755)

	// Copy onboarding/trust state so Claude skips first-run setup
	seedTestHome(tmpDir)

	// Empty MCP config to block all MCP servers
	os.WriteFile(filepath.Join(configDir, "mcp-config.json"), []byte(`{"mcpServers":{}}`), 0o644)

	return &IsolatedEnv{HomeDir: tmpDir, ConfigDir: configDir}, nil
}

// MCPConfigPath returns the path to the empty MCP config file.
func (e *IsolatedEnv) MCPConfigPath() string {
	return filepath.Join(e.ConfigDir, "mcp-config.json")
}

// SettingsPath returns the path to settings.json in the config dir.
func (e *IsolatedEnv) SettingsPath() string {
	return filepath.Join(e.ConfigDir, "settings.json")
}

// WriteSettings writes a JSON settings file to the config dir.
func (e *IsolatedEnv) WriteSettings(data []byte) error {
	return os.WriteFile(e.SettingsPath(), data, 0o644)
}

// Script builds a shell script that runs claude in this isolated env.
// Extra args are appended to the claude command.
func (e *IsolatedEnv) Script(extraArgs ...string) string {
	args := strings.Join(extraArgs, " ")
	mcpArgs := fmt.Sprintf("--mcp-config %s --strict-mcp-config", ShellQuote(e.MCPConfigPath()))
	claudeCmd := "claude " + mcpArgs
	if args != "" {
		claudeCmd += " " + args
	}
	// Create an editor wrapper that restores real HOME so vim/nvim
	// can find its config, then reverts HOME for Claude.
	realHome, _ := os.UserHomeDir()
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	wrapperPath := filepath.Join(e.HomeDir, "editor.sh")
	wrapperContent := fmt.Sprintf("#!/bin/bash\nHOME=%s exec %s \"$@\"\n",
		ShellQuote(realHome), editor)
	os.WriteFile(wrapperPath, []byte(wrapperContent), 0o755)

	// Resolve symlinks for HOME and cd path (macOS /tmp → /private/tmp)
	// so Claude's project path matches the trust entry in .claude.json.
	resolvedHome := e.HomeDir
	if r, err := filepath.EvalSymlinks(e.HomeDir); err == nil {
		resolvedHome = r
	}

	return fmt.Sprintf(
		`unset CLAUDECODE; %sexport REAL_HOME=%s; export EDITOR=%s; export HOME=%s; cd %s; %s; `+
			`rc=$?; if [ $rc -ne 0 ]; then echo ""; echo "[claude exited: $rc] press any key"; read -n1; fi`,
		OAuthTokenEnv(), ShellQuote(realHome), ShellQuote(wrapperPath), ShellQuote(resolvedHome), ShellQuote(resolvedHome), claudeCmd,
	)
}

// RunPopup launches the script in a tmux display-popup with a nested tmux
// session for scrollback support. Blocks until the popup exits.
func (e *IsolatedEnv) RunPopup(script string) {
	// Write script to file to avoid quoting issues with nested tmux
	scriptPath := filepath.Join(e.HomeDir, "run.sh")
	os.WriteFile(scriptPath, []byte("#!/bin/bash\n"+script+"\n"), 0o755)

	// Nested tmux session enables mouse scroll and scrollback in the popup.
	// status off hides the inner status bar; mouse on enables scroll.
	sessionName := "ccx-test-" + filepath.Base(e.HomeDir)
	nestedCmd := fmt.Sprintf(
		"tmux new-session -s %s 'bash %s' \\; set status off \\; set mouse on",
		ShellQuote(sessionName), ShellQuote(scriptPath),
	)
	exec.Command("tmux", "display-popup", "-E", "-w", "90%", "-h", "80%",
		"bash", "-c", nestedCmd).Run()
}

// Cleanup removes the temp directory.
func (e *IsolatedEnv) Cleanup() {
	os.RemoveAll(e.HomeDir)
}

// ExtractClaudeOAuthToken reads the OAuth access token from the macOS Keychain.
func ExtractClaudeOAuthToken() (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return "", err
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(out, &creds); err != nil {
		return "", err
	}
	if creds.ClaudeAiOauth.AccessToken == "" {
		return "", fmt.Errorf("no access token found in keychain")
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}

// OAuthTokenEnv returns a shell snippet to export CLAUDE_CODE_OAUTH_TOKEN.
// Checks env first, then falls back to extracting from macOS Keychain.
func OAuthTokenEnv() string {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		token, _ = ExtractClaudeOAuthToken()
	}
	if token != "" {
		return fmt.Sprintf("export CLAUDE_CODE_OAUTH_TOKEN=%s; ", ShellQuote(token))
	}
	return ""
}

// seedTestHome copies onboarding/trust state into an isolated HOME directory
// so Claude skips first-run setup in test environments.
// It copies both ~/.claude/.claude.json → fakeHome/.claude/.claude.json
// and ~/.claude.json → fakeHome/.claude.json (Claude reads both locations).
func seedTestHome(fakeHome string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// Copy config-dir level state (inside .claude/)
	configDir := filepath.Join(fakeHome, ".claude")
	os.MkdirAll(configDir, 0o755)
	if data, err := os.ReadFile(filepath.Join(home, ".claude", ".claude.json")); err == nil {
		os.WriteFile(filepath.Join(configDir, ".claude.json"), data, 0o644)
	}

	// Copy remote-settings.json — this caches the approved managed settings.
	// Claude compares new managed settings against this cache; if unchanged,
	// the "Managed settings require approval" prompt is skipped.
	if data, err := os.ReadFile(filepath.Join(home, ".claude", "remote-settings.json")); err == nil {
		os.WriteFile(filepath.Join(configDir, "remote-settings.json"), data, 0o644)
	}

	// Copy home-level state (at HOME/.claude.json) and inject trust for
	// the fake HOME path so the "Managed settings require approval" prompt
	// is skipped in test environments.
	// Resolve symlinks (macOS /tmp → /private/tmp) because Claude uses the
	// real path as the project key, not the symlink path.
	if data, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
		realFakeHome := fakeHome
		if resolved, err := filepath.EvalSymlinks(fakeHome); err == nil {
			realFakeHome = resolved
		}
		data = injectProjectTrust(data, realFakeHome)
		// Also inject under the unresolved path in case Claude uses it
		if realFakeHome != fakeHome {
			data = injectProjectTrust(data, fakeHome)
		}
		os.WriteFile(filepath.Join(fakeHome, ".claude.json"), data, 0o644)
	}
}

// injectProjectTrust adds hasTrustDialogAccepted=true for the given project
// path inside a .claude.json blob. This prevents the managed settings approval
// prompt from appearing in isolated test environments.
func injectProjectTrust(data []byte, projectPath string) []byte {
	var state map[string]json.RawMessage
	if json.Unmarshal(data, &state) != nil {
		return data
	}

	// Parse the "projects" sub-object
	var projects map[string]map[string]interface{}
	raw, ok := state["projects"]
	if !ok {
		projects = make(map[string]map[string]interface{})
	} else if json.Unmarshal(raw, &projects) != nil {
		return data
	}

	// Ensure the project entry exists with trust accepted
	entry, exists := projects[projectPath]
	if !exists {
		entry = map[string]interface{}{
			"allowedTools":                  []interface{}{},
			"mcpContextUris":                []interface{}{},
			"mcpServers":                    map[string]interface{}{},
			"hasCompletedProjectOnboarding": true,
			"projectOnboardingSeenCount":    10,
		}
	}
	entry["hasTrustDialogAccepted"] = true
	entry["hasCompletedProjectOnboarding"] = true
	projects[projectPath] = entry

	// Write back
	projBytes, err := json.Marshal(projects)
	if err != nil {
		return data
	}
	state["projects"] = projBytes

	out, err := json.Marshal(state)
	if err != nil {
		return data
	}
	return out
}
