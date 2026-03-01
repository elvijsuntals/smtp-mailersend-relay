package dispatch

import (
	"testing"
	"time"
)

func TestClassifyFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "network", status: 0, want: "retry"},
		{name: "429", status: 429, want: "retry"},
		{name: "500", status: 500, want: "retry"},
		{name: "422", status: 422, want: "dlq"},
		{name: "400", status: 400, want: "dlq"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyFailure(tc.status, assertErr{})
			if got != tc.want {
				t.Fatalf("classifyFailure(%d) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestJitteredBackoffRange(t *testing.T) {
	t.Parallel()
	base := backoffSchedule[0]
	for i := 0; i < 100; i++ {
		got := jitteredBackoff(1)
		min := time.Duration(float64(base) * 0.8)
		max := time.Duration(float64(base) * 1.2)
		if got < min || got > max {
			t.Fatalf("backoff out of range: got=%s min=%s max=%s", got, min, max)
		}
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "x" }

