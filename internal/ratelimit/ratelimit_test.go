package ratelimit

import (
	"testing"
	"time"
)

func TestAllowAndReset(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(2, time.Minute).WithClock(func() time.Time { return now })
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("event %d denied", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok || retry != time.Minute {
		t.Fatalf("third event: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("other key should be independent")
	}
	now = now.Add(time.Minute)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("window should have reset")
	}
	now = now.Add(2 * time.Minute)
	l.Prune()
	if len(l.buckets) != 0 {
		t.Fatalf("prune left %d buckets", len(l.buckets))
	}
}
