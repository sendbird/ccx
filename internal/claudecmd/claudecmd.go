package claudecmd

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode"
)

const (
	DefaultTemplate = "claude {{args}}"
	argsPlaceholder = "{{args}}"
)

type Config struct {
	CommandTemplate string `yaml:"command_template,omitempty"`
}

func RenderArgv(cfg Config, args ...string) ([]string, error) {
	template := strings.TrimSpace(cfg.CommandTemplate)
	if template == "" {
		template = DefaultTemplate
	}

	parts, err := splitTemplate(template)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("claude command template is empty")
	}

	insertedArgs := false
	argv := make([]string, 0, len(parts)+len(args))
	for _, part := range parts {
		if strings.Contains(part, argsPlaceholder) && part != argsPlaceholder {
			return nil, fmt.Errorf("%s must be its own argument", argsPlaceholder)
		}
		if part == argsPlaceholder {
			argv = append(argv, args...)
			insertedArgs = true
			continue
		}
		argv = append(argv, part)
	}
	if !insertedArgs {
		argv = append(argv, args...)
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("claude command template has no executable")
	}
	return argv, nil
}

func Command(cfg Config, dir string, args ...string) (*exec.Cmd, error) {
	argv, err := RenderArgv(cfg, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd, nil
}

func ShellCommand(cfg Config, dir string, args ...string) (string, error) {
	argv, err := RenderArgv(cfg, args...)
	if err != nil {
		return "", err
	}
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, ShellQuote(arg))
	}
	cmd := strings.Join(quoted, " ")
	if dir != "" {
		cmd = "cd " + ShellQuote(dir) + " && " + cmd
	}
	return cmd, nil
}

func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func splitTemplate(input string) ([]string, error) {
	var tokens []string
	var b strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	hadQuotedOrEscaped := false

	flush := func() {
		if b.Len() == 0 && !hadQuotedOrEscaped {
			return
		}
		tokens = append(tokens, b.String())
		b.Reset()
		hadQuotedOrEscaped = false
	}

	for _, r := range input {
		if escaped {
			b.WriteRune(r)
			escaped = false
			hadQuotedOrEscaped = true
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if inSingle {
			if r == '\'' {
				inSingle = false
				hadQuotedOrEscaped = true
				continue
			}
			b.WriteRune(r)
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
				hadQuotedOrEscaped = true
				continue
			}
			b.WriteRune(r)
			continue
		}

		switch {
		case unicode.IsSpace(r):
			flush()
		case r == '\'':
			inSingle = true
			hadQuotedOrEscaped = true
		case r == '"':
			inDouble = true
			hadQuotedOrEscaped = true
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		return nil, errors.New("claude command template ends with an unfinished escape")
	}
	if inSingle || inDouble {
		return nil, errors.New("claude command template has an unterminated quote")
	}
	flush()
	return tokens, nil
}
