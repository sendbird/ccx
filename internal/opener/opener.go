// Package opener resolves how ccx opens URLs. By default it hands the URL to
// the OS default handler (open / xdg-open), but a config-driven command
// template lets users route URLs anywhere — e.g. "tmux-chrome open {{url}}".
package opener

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/sendbird/ccx/internal/claudecmd"
)

const urlPlaceholder = "{{url}}"

// Config controls how URLs are opened.
type Config struct {
	// CommandTemplate is a shell-like command template with a {{url}}
	// placeholder, e.g. "tmux-chrome open {{url}}". When empty, ccx falls back
	// to the OS default opener (open on macOS, xdg-open on Linux). If the
	// template omits {{url}}, the URL is appended as the final argument.
	CommandTemplate string `yaml:"command_template,omitempty"`
}

// RenderArgv builds the argv used to open u for the given config. With an empty
// template it returns the OS default opener argv.
func RenderArgv(cfg Config, u string) ([]string, error) {
	template := strings.TrimSpace(cfg.CommandTemplate)
	if template == "" {
		return defaultArgv(u)
	}

	parts, err := claudecmd.SplitTemplate(template)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("open command template is empty")
	}

	insertedURL := false
	argv := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		if strings.Contains(part, urlPlaceholder) && part != urlPlaceholder {
			return nil, fmt.Errorf("%s must be its own argument", urlPlaceholder)
		}
		if part == urlPlaceholder {
			argv = append(argv, u)
			insertedURL = true
			continue
		}
		argv = append(argv, part)
	}
	if !insertedURL {
		argv = append(argv, u)
	}
	if argv[0] == "" {
		return nil, errors.New("open command template has no executable")
	}
	return argv, nil
}

// defaultArgv returns the OS default opener argv for u.
func defaultArgv(u string) ([]string, error) {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", u}, nil
	case "linux":
		return []string{"xdg-open", u}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// Open launches the configured command (or OS default) to open u.
func Open(cfg Config, u string) error {
	argv, err := RenderArgv(cfg, u)
	if err != nil {
		return err
	}
	return exec.Command(argv[0], argv[1:]...).Start()
}
