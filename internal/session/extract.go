package session

import (
	"bytes"
	"time"
)

// extractJSONString reads a JSON string value starting after the opening quote.
// It handles escape sequences and limits scanning to 200 bytes.
func extractJSONString(b []byte) string {
	var buf []byte
	limit := min(len(b), 200)
	for i := 0; i < limit; i++ {
		if b[i] == '\\' && i+1 < limit {
			next := b[i+1]
			switch next {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case 'n':
				buf = append(buf, '\n')
			case 't':
				buf = append(buf, '\t')
			case 'r':
				buf = append(buf, '\r')
			default:
				buf = append(buf, '\\', next)
			}
			i++
			continue
		}
		if b[i] == '"' {
			return string(buf)
		}
		buf = append(buf, b[i])
	}
	if len(buf) > 0 {
		return string(buf)
	}
	return ""
}

// extractTimestamp finds a "timestamp":"..." field in a line and parses it.
func extractTimestamp(line []byte) time.Time {
	markers := [][]byte{[]byte(`"timestamp":"`), []byte(`"timestamp": "`)}
	for _, marker := range markers {
		idx := bytes.Index(line, marker)
		if idx < 0 {
			continue
		}
		start := idx + len(marker)
		if start+40 > len(line) {
			continue
		}
		end := bytes.IndexByte(line[start:], '"')
		if end <= 0 || end > 40 {
			continue
		}
		tsStr := string(line[start : start+end])
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			return t
		}
	}
	return time.Time{}
}

// extractStringField extracts a JSON string value using two marker variants (with/without space).
func extractStringField(line []byte, marker1, marker2 []byte) string {
	idx := bytes.Index(line, marker1)
	markerLen := len(marker1)
	if idx < 0 {
		idx = bytes.Index(line, marker2)
		markerLen = len(marker2)
	}
	if idx < 0 {
		return ""
	}
	start := idx + markerLen
	return extractJSONString(line[start:])
}

// extractJSONFieldValue skips to the colon after a JSON key, then extracts the quoted string value.
func extractJSONFieldValue(b []byte) string {
	i := 0
	for i < len(b) && b[i] != ':' {
		i++
	}
	i++ // skip colon
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	if i >= len(b) || b[i] != '"' {
		return ""
	}
	i++ // skip opening quote
	start := i
	for i < len(b) {
		if b[i] == '\\' {
			i += 2
			continue
		}
		if b[i] == '"' {
			return string(b[start:i])
		}
		i++
	}
	return ""
}

// extractQuotedAfter finds one of the markers in line and returns the quoted string value after it.
func extractQuotedAfter(line []byte, markers ...[]byte) string {
	for _, m := range markers {
		idx := bytes.Index(line, m)
		if idx < 0 {
			continue
		}
		start := idx + len(m) // points right after the opening quote
		end := bytes.IndexByte(line[start:], '"')
		if end > 0 {
			return string(line[start : start+end])
		}
	}
	return ""
}

// lastStringField finds the LAST occurrence of a marker and extracts the JSON string value.
func lastStringField(line []byte, marker1, marker2 []byte) string {
	idx := bytes.LastIndex(line, marker1)
	markerLen := len(marker1)
	if idx2 := bytes.LastIndex(line, marker2); idx2 > idx {
		idx = idx2
		markerLen = len(marker2)
	}
	if idx < 0 {
		return ""
	}
	start := idx + markerLen
	return extractJSONString(line[start:])
}

// countOccurrences counts non-overlapping occurrences of pattern in data.
func countOccurrences(data, pattern []byte) int {
	count := 0
	offset := 0
	for {
		idx := bytes.Index(data[offset:], pattern)
		if idx < 0 {
			break
		}
		count++
		offset += idx + len(pattern)
	}
	return count
}

// contains checks if substr appears in s (byte-level, no strings import needed).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
