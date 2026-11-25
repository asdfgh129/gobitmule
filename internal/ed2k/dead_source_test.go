package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// A stale KAD source accepts TCP, sees our Hello and closes without answering.
// The generic FailCount backoff retries it every 20-40s for ten minutes; the
// dead-source quarantine must take it out of the rotation after one strike.
func TestSilentDisconnectQuarantinesDeadSource(t *testing.T) {
	peer := NewPeer(protocol.NewEndpoint(0x04030201, 4662))
	conn := &PeerConnection{
		peerInfo:       &peer,
		connectStarted: CurrentTime() - Seconds(5),
	}
	conn.markDeadSourceIfNeeded(EndOfStream)

	if peer.DeadStrikes != 1 {
		t.Fatalf("dead strikes = %d, want 1", peer.DeadStrikes)
	}
	if peer.DeadUntil <= CurrentTime() {
		t.Fatalf("dead source was not quarantined (until=%d)", peer.DeadUntil)
	}
	// A second silent failure escalates the cooldown.
	firstUntil := peer.DeadUntil
	conn.markDeadSourceIfNeeded(IOException)
	if peer.DeadStrikes != 2 || peer.DeadUntil <= firstUntil {
		t.Fatalf("cooldown did not escalate: strikes=%d until=%d first=%d",
			peer.DeadStrikes, peer.DeadUntil, firstUntil)
	}
}

// Non-silent failures (timeouts, explicit closes) keep the normal backoff;
// quarantine is only for the "connected, not a single byte back" pattern.
func TestOtherDisconnectDoesNotQuarantine(t *testing.T) {
	peer := NewPeer(protocol.NewEndpoint(0x04030201, 4662))
	conn := &PeerConnection{peerInfo: &peer, connectStarted: CurrentTime() - Seconds(5)}
	conn.markDeadSourceIfNeeded(ConnectionTimeout)
	if peer.DeadStrikes != 0 || peer.DeadUntil != 0 {
		t.Fatalf("normal disconnect was quarantined: %+v", peer)
	}

	// A peer that answered Hello is alive even if the transfer later fails.
	conn.helloAnswered = true
	conn.markDeadSourceIfNeeded(EndOfStream)
	if peer.DeadStrikes != 0 {
		t.Fatal("answered peer must not be treated as dead")
	}
	// Conversely, a real HelloAnswer clears a previous quarantine.
	peer.DeadStrikes, peer.DeadUntil = 2, CurrentTime()+Seconds(600)
	conn.clearDeadSourceStrikes()
	if peer.DeadStrikes != 0 || peer.DeadUntil != 0 {
		t.Fatalf("handshake did not clear quarantine: %+v", peer)
	}
}

func TestPolicySkipsQuarantinedPeer(t *testing.T) {
	now := CurrentTime()
	peer := NewPeerWithSource(protocol.NewEndpoint(0x04030201, 4662), true, int(PeerDHT))
	peer.DeadUntil = now + Seconds(120)
	p := &Policy{peers: []Peer{peer}}

	if got := p.FindConnectCandidate(now); got != nil {
		t.Fatalf("quarantined peer was selected: %+v", got)
	}
	// Once the quarantine expires the same endpoint becomes eligible again.
	if got := p.FindConnectCandidate(now + Seconds(121)); got == nil {
		t.Fatal("peer was not reconsidered after quarantine expiry")
	}
}
