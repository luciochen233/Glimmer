package server

import (
	"testing"
	"time"
)

func TestGlobalRateLimiterBurstAndRefill(t *testing.T) {
	g := newGlobalRateLimiter(10, 10)

	for i := range 10 {
		if !g.Allow() {
			t.Fatalf("expected first 10 allows to succeed (burst); allow #%d denied", i+1)
		}
	}
	if g.Allow() {
		t.Fatal("expected allow to be denied once burst is exhausted")
	}

	// At 10 tokens/sec, ~250ms refills ~2 tokens.
	time.Sleep(250 * time.Millisecond)
	allowed := 0
	for range 4 {
		if g.Allow() {
			allowed++
		}
	}
	if allowed < 1 || allowed > 3 {
		t.Fatalf("expected ~2 tokens after 250ms refill, got %d allows", allowed)
	}
}
