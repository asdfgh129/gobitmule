package goed2k

import (
	"testing"
	"time"
)

// PumpIO hands the write deadline straight to net.Conn.SetWriteDeadline as a
// time.Duration (nanoseconds). Large parts need seconds to drain to a slow
// peer, so guard the unit explicitly.
func TestConnectionIODeadlineIsSeconds(t *testing.T) {
	d := time.Duration(connectionIODeadlineNS)
	if d < time.Second {
		t.Fatalf("connection write deadline is %s, want at least 1 second", d)
	}
	if d != 5*time.Second {
		t.Fatalf("connection write deadline is %s, want 5s", d)
	}
}

// The read side must stay short: PumpIO visits sockets serially, and a 5s
// idle read would delay the accepted server callback's HelloAnswer by more
// than the server's ~4s port-test timeout.
func TestConnectionPollDeadlineIsShort(t *testing.T) {
	d := time.Duration(connectionPollDeadlineNS)
	if d < 10*time.Millisecond || d > 200*time.Millisecond {
		t.Fatalf("connection read poll deadline is %s, want 10ms..200ms", d)
	}
}
