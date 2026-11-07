package run

import "testing"

func TestNewUsageAndAdd(t *testing.T) {
	u := NewUsage(100, 20, 80, 0)
	if u.TotalTokens != u.InputTokens+u.OutputTokens {
		t.Fatalf("total=%d input=%d output=%d", u.TotalTokens, u.InputTokens, u.OutputTokens)
	}
	if u.CacheReadTokens+u.CacheWriteTokens > u.InputTokens {
		t.Fatalf("cache exceeds input: %+v", u)
	}

	var sum Usage
	sum.Add(u)
	sum.Add(NewUsage(10, 4, 3, 2))
	want := NewUsage(110, 24, 83, 2)
	if sum != want {
		t.Fatalf("sum=%+v want %+v", sum, want)
	}
}
