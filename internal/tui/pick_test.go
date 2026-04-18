package tui

import "testing"

func TestPickResultSumType(t *testing.T) {
	var _ PickResult = SessionsResult{}
}
