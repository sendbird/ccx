package extract

import "testing"

func TestCleanURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"plain PR", "https://github.com/sendbird/core-k8s/pull/741", "https://github.com/sendbird/core-k8s/pull/741"},
		{"trailing double star", "https://github.com/sendbird/delight-core-k8s/pull/29**", "https://github.com/sendbird/delight-core-k8s/pull/29"},
		{"trailing single star", "https://github.com/sendbird/core-k8s/pull/741*", "https://github.com/sendbird/core-k8s/pull/741"},
		{"trailing period", "https://github.com/sendbird/core-k8s/pull/741.", "https://github.com/sendbird/core-k8s/pull/741"},
		{"trailing parens", "https://github.com/sendbird/core-k8s/pull/741)", "https://github.com/sendbird/core-k8s/pull/741"},
		{"trailing mixed", "https://github.com/sendbird/core-k8s/pull/741**.", "https://github.com/sendbird/core-k8s/pull/741"},
		{"no host", "https://", ""},
		{"bare domain", "https://github.com/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanURL(tt.raw)
			if got != tt.want {
				t.Errorf("CleanURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
