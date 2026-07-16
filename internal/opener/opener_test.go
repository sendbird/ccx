package opener

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestRenderArgvDefaultUsesOSOpener(t *testing.T) {
	got, err := RenderArgv(Config{}, "https://example.com")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	var want []string
	switch runtime.GOOS {
	case "darwin":
		want = []string{"open", "https://example.com"}
	case "linux":
		want = []string{"xdg-open", "https://example.com"}
	default:
		t.Skipf("unsupported platform: %s", runtime.GOOS)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvTemplateWithPlaceholder(t *testing.T) {
	got, err := RenderArgv(Config{CommandTemplate: "tmux-chrome open {{url}}"}, "https://example.com")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"tmux-chrome", "open", "https://example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvAppendsURLWhenPlaceholderMissing(t *testing.T) {
	got, err := RenderArgv(Config{CommandTemplate: "firefox --new-tab"}, "https://example.com")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"firefox", "--new-tab", "https://example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvQuotedExecutable(t *testing.T) {
	got, err := RenderArgv(Config{CommandTemplate: "'/opt/my browser/open' {{url}}"}, "https://example.com")
	if err != nil {
		t.Fatalf("RenderArgv failed: %v", err)
	}
	want := []string{"/opt/my browser/open", "https://example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRenderArgvRejectsEmbeddedPlaceholder(t *testing.T) {
	_, err := RenderArgv(Config{CommandTemplate: "open --url={{url}}"}, "https://example.com")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "own argument") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRenderArgvRejectsUnterminatedQuote(t *testing.T) {
	_, err := RenderArgv(Config{CommandTemplate: "open 'unterminated"}, "https://example.com")
	if err == nil {
		t.Fatal("expected error")
	}
}
