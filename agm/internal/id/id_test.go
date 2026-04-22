package id

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewSessionID_Prefix(t *testing.T) {
	sid := NewSessionID()
	if !strings.HasPrefix(sid, "sess_") {
		t.Fatalf("want sess_ prefix, got %q", sid)
	}
}

func TestNewSessionID_Length(t *testing.T) {
	sid := NewSessionID()
	// "sess_" (5) + 6 bytes hex-encoded (12) = 17
	if len(sid) != 17 {
		t.Fatalf("want length 17, got %d (%q)", len(sid), sid)
	}
}

func TestNewSessionID_HexSuffix(t *testing.T) {
	re := regexp.MustCompile(`^sess_[0-9a-f]{12}$`)
	for i := 0; i < 20; i++ {
		sid := NewSessionID()
		if !re.MatchString(sid) {
			t.Fatalf("id %q does not match %s", sid, re)
		}
	}
}

func TestNewSessionID_Unique(t *testing.T) {
	const n = 10000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		sid := NewSessionID()
		if _, dup := seen[sid]; dup {
			t.Fatalf("collision after %d ids: %s", i, sid)
		}
		seen[sid] = struct{}{}
	}
}
