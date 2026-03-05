package session

import (
	"testing"
)

func TestEncodeProjectPath(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"/Users/test/project", "-Users-test-project"},
		{"/Users/test/my.project", "-Users-test-my-project"},
		{"/tmp/a/b/c", "-tmp-a-b-c"},
		{"", ""},
	}
	for _, tt := range tests {
		got := EncodeProjectPath(tt.input)
		if got != tt.want {
			t.Errorf("EncodeProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		path, home, want string
	}{
		{"/Users/test/project", "/Users/test", "~/project"},
		{"/Users/test", "/Users/test", "~"},
		{"/other/path", "/Users/test", "/other/path"},
		{"", "/Users/test", ""},
	}
	for _, tt := range tests {
		got := ShortenPath(tt.path, tt.home)
		if got != tt.want {
			t.Errorf("ShortenPath(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
		}
	}
}

func TestDecodeDirName(t *testing.T) {
	tests := []struct {
		dirName, home, want string
	}{
		{"-Users-test-project", "/Users/test", "~/project"},
		{"plain-dir", "/Users/test", "plain-dir"},
		{"-tmp-something", "/Users/test", "/tmp/something"},
	}
	for _, tt := range tests {
		got := decodeDirName(tt.dirName, tt.home)
		if got != tt.want {
			t.Errorf("decodeDirName(%q, %q) = %q, want %q", tt.dirName, tt.home, got, tt.want)
		}
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	// EncodeProjectPath replaces / and . with -
	// decodeDirName replaces - with / (simple decode)
	// This is a lossy transform, but we can verify encode produces expected output
	paths := []string{
		"/Users/gavin/src/project",
		"/home/user/workspace",
		"/tmp/test",
	}
	for _, p := range paths {
		encoded := EncodeProjectPath(p)
		if encoded == "" {
			t.Errorf("EncodeProjectPath(%q) returned empty", p)
		}
		if encoded[0] != '-' {
			t.Errorf("encoded path should start with -, got %q", encoded)
		}
	}
}
