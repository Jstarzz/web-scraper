package main

import (
	"testing"
	"time"
)

func TestRetryDelayBacksOffAndCaps(t *testing.T) {
	first := retryDelay(time.Second, 1)
	if first < time.Second || first >= 1500*time.Millisecond {
		t.Fatalf("first retry delay out of range: %s", first)
	}

	second := retryDelay(time.Second, 2)
	if second < 2*time.Second || second >= 2500*time.Millisecond {
		t.Fatalf("second retry delay out of range: %s", second)
	}

	capped := retryDelay(10*time.Second, 5)
	if capped < 15*time.Second || capped >= 15500*time.Millisecond {
		t.Fatalf("capped retry delay out of range: %s", capped)
	}
}
