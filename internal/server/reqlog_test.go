package server

import (
	"testing"
)

func TestRequestLogCapsAndOrders(t *testing.T) {
	l := newRequestLog(3)
	for i := range 5 {
		l.Add(requestEntry{Status: i})
	}

	recent := l.Recent()
	if len(recent) != 3 {
		t.Fatalf("expected cap of 3 entries, got %d", len(recent))
	}
	// Newest first: last added (status=4) should be first.
	if recent[0].Status != 4 {
		t.Fatalf("expected newest entry first (status=4), got %d", recent[0].Status)
	}
	if recent[2].Status != 2 {
		t.Fatalf("expected oldest retained entry to be status=2, got %d", recent[2].Status)
	}

	// Recent must return a copy — mutating it must not affect the buffer.
	recent[0].Status = 999
	if l.Recent()[0].Status != 4 {
		t.Fatalf("Recent() did not return a copy")
	}
}

func TestShouldLogRequestExcludesNoise(t *testing.T) {
	cases := map[string]bool{
		"/static/css/style.css":  false,
		"/uploads/thumb/abc.png": false,
		"/healthz":               false,
		"/favicon.ico":           false,
		"/":                      true,
		"/shorten":               true,
		"/admin":                 true,
		"/api/links":             true,
		"/uploads/abc.jpg":       true,
	}
	for path, want := range cases {
		if got := shouldLogRequest(path); got != want {
			t.Errorf("shouldLogRequest(%q) = %v, want %v", path, got, want)
		}
	}
}
