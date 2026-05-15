package claudecmd

import (
	"reflect"
	"strings"
	"testing"
)

func TestRenderArgvDefault(t *testing.T) {
	got, err := RenderArgv(Config{}, "--resume", "abc")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"claude", "--resume", "abc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvWrapperTemplate(t *testing.T) {
	got, err := RenderArgv(Config{CommandTemplate: "ccproxy -- claude {{args}}"}, "--resume", "abc")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"ccproxy", "--", "claude", "--resume", "abc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvAppendsArgsWhenPlaceholderMissing(t *testing.T) {
	got, err := RenderArgv(Config{CommandTemplate: "claude --model opus"}, "--resume", "abc")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"claude", "--model", "opus", "--resume", "abc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvQuotedExecutable(t *testing.T) {
	got, err := RenderArgv(Config{CommandTemplate: "'/opt/my wrapper/claude' --flag {{args}}"}, "plugin", "install", "foo/bar")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"/opt/my wrapper/claude", "--flag", "plugin", "install", "foo/bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvRejectsEmbeddedArgsPlaceholder(t *testing.T) {
	_, err := RenderArgv(Config{CommandTemplate: "claude --wrapped={{args}}"}, "--resume", "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "own argument") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderArgvRejectsUnterminatedQuote(t *testing.T) {
	_, err := RenderArgv(Config{CommandTemplate: "claude 'unterminated"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestShellCommandQuotesArguments(t *testing.T) {
	got, err := ShellCommand(Config{CommandTemplate: "ccproxy -- claude {{args}}"}, "/tmp/a dir", "--resume", "abc; touch /tmp/pwned")
	if err != nil {
		t.Fatalf("ShellCommand failed: %v", err)
	}
	want := "cd '/tmp/a dir' && 'ccproxy' '--' 'claude' '--resume' 'abc; touch /tmp/pwned'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCommandSetsDirAndArgs(t *testing.T) {
	cmd, err := Command(Config{CommandTemplate: "ccproxy -- claude {{args}}"}, "/tmp", "--resume", "abc")
	if err != nil {
		t.Fatalf("Command failed: %v", err)
	}
	if cmd.Args[0] != "ccproxy" {
		t.Fatalf("Args[0] = %q", cmd.Args[0])
	}
	want := []string{"ccproxy", "--", "claude", "--resume", "abc"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Args = %#v, want %#v", cmd.Args, want)
	}
	if cmd.Dir != "/tmp" {
		t.Fatalf("Dir = %q", cmd.Dir)
	}
}
