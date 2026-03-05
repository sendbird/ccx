package session

import (
	"testing"
	"time"
)

func TestExtractJSONString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", `hello"rest`, "hello"},
		{"escaped quote", `say \"hi\""rest`, `say "hi"`},
		{"escaped backslash", `path\\to\\file"`, `path\to\file`},
		{"escaped newline", `line1\nline2"`, "line1\nline2"},
		{"escaped tab", `col1\tcol2"`, "col1\tcol2"},
		{"escaped r", `line\rend"`, "line\rend"},
		{"unknown escape", `test\xval"`, `test\xval`},
		{"empty string", `"rest`, ""},
		{"no closing quote", `hello world`, "hello world"},
		{"empty input", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONString([]byte(tt.input))
			if got != tt.want {
				t.Errorf("extractJSONString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractJSONString_LongInput(t *testing.T) {
	// Input longer than 200 byte limit
	long := make([]byte, 250)
	for i := range long {
		long[i] = 'a'
	}
	got := extractJSONString(long)
	if len(got) != 200 {
		t.Errorf("length = %d, want 200 (capped at limit)", len(got))
	}
}

func TestExtractTimestamp(t *testing.T) {
	tests := []struct {
		name string
		line string
		zero bool
	}{
		{"compact", `{"timestamp":"2025-01-15T10:30:00Z","other":"val","padding":"extra-data-to-reach-minimum-length"}`, false},
		{"spaced", `{"timestamp": "2025-06-01T08:00:00.123Z","other":"val","padding":"extra-data-to-reach-minimum"}`, false},
		{"no timestamp", `{"role":"user","content":"hi"}`, true},
		{"invalid timestamp", `{"timestamp":"not-a-time","other":"val","padding":"extra-data-to-reach-minimum-length"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTimestamp([]byte(tt.line))
			if tt.zero && !got.IsZero() {
				t.Errorf("expected zero time, got %v", got)
			}
			if !tt.zero && got.IsZero() {
				t.Error("expected non-zero time")
			}
		})
	}
}

func TestExtractTimestamp_RFC3339Nano(t *testing.T) {
	line := `{"timestamp":"2025-06-15T10:30:00.123456789Z","padding":"extra-data-to-reach-minimum-length"}`
	got := extractTimestamp([]byte(line))
	want := time.Date(2025, 6, 15, 10, 30, 0, 123456789, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExtractStringField(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		marker1 string
		marker2 string
		want    string
	}{
		{"compact", `{"name":"Read","type":"tool"}`, `"name":"`, `"name": "`, "Read"},
		{"spaced", `{"name": "Edit","type":"tool"}`, `"name":"`, `"name": "`, "Edit"},
		{"not found", `{"type":"tool"}`, `"name":"`, `"name": "`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStringField([]byte(tt.line), []byte(tt.marker1), []byte(tt.marker2))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractJSONFieldValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"with colon", `"name":"Read"`, "Read"},
		{"with space", `"name": "Edit"`, "Edit"},
		{"no colon", `no colon here`, ""},
		{"no quote after colon", `"name": 42`, ""},
		{"empty", ``, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONFieldValue([]byte(tt.input))
			if got != tt.want {
				t.Errorf("extractJSONFieldValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractQuotedAfter(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		markers []string
		want    string
	}{
		{"first marker", `{"skill":"commit","args":""}`, []string{`"skill":"`}, "commit"},
		{"second marker", `{"skill": "commit","args":""}`, []string{`"skill":"`, `"skill": "`}, "commit"},
		{"not found", `{"type":"tool"}`, []string{`"skill":"`}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			markers := make([][]byte, len(tt.markers))
			for i, m := range tt.markers {
				markers[i] = []byte(m)
			}
			got := extractQuotedAfter([]byte(tt.line), markers...)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLastStringField(t *testing.T) {
	line := `{"name":"first","other":"x","name":"last"}`
	got := lastStringField([]byte(line), []byte(`"name":"`), []byte(`"name": "`))
	if got != "last" {
		t.Errorf("got %q, want %q", got, "last")
	}
}

func TestCountOccurrences(t *testing.T) {
	tests := []struct {
		data    string
		pattern string
		want    int
	}{
		{`"type":"tool_use","type":"tool_use"`, `"type":"tool_use"`, 2},
		{`no match here`, `"type":"tool_use"`, 0},
		{`aaa`, `a`, 3},
		{``, `a`, 0},
	}
	for _, tt := range tests {
		got := countOccurrences([]byte(tt.data), []byte(tt.pattern))
		if got != tt.want {
			t.Errorf("countOccurrences(%q, %q) = %d, want %d", tt.data, tt.pattern, got, tt.want)
		}
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		s, substr string
		want      bool
	}{
		{"hello world", "world", true},
		{"hello world", "xyz", false},
		{"", "a", false},
		{"a", "a", true},
		{"abc", "", true},
	}
	for _, tt := range tests {
		got := contains(tt.s, tt.substr)
		if got != tt.want {
			t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}
