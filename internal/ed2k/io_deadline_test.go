package goed2k

import (
	"testing"
	"time"
)

// PumpIO hands the IO deadline straight to net.Conn.SetReadDeadline as a
// time.Duration (nanoseconds). eD2k HelloAnswer packets arrive ~200-400ms
// after our Hello; a millisecond-scale deadline silently drops every reply
// and makes live KAD sources look dead. Guard the unit explicitly.
func TestConnectionIODeadlineIsSeconds(t *testing.T) {
	d := time.Duration(connectionIODeadlineNS)
	if d < time.Second {
		t.Fatalf("connection IO deadline is %s, want at least 1 second", d)
	}
	if d != 5*time.Second {
		t.Fatalf("connection IO deadline is %s, want 5s", d)
	}
}
