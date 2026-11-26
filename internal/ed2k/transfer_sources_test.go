package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func testEndpoint(last byte) protocol.Endpoint {
	return protocol.NewEndpoint(int32(0x0A000001+(uint32(last)<<8)), 4662+int(last))
}

// eMule's client list shows every known source with its state. Reporting only
// live sockets made a task with many discovered sources look like it had a
// single source, and hid why the rest were idle.
func TestGetPeersInfoIncludesIdleSources(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       190 * 1024 * 4,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}

	now := CurrentTime()
	// A healthy candidate that has not been dialed yet.
	if err := transfer.AddPeer(testEndpoint(1), int(PeerServer)); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	// A candidate backed off after a failure.
	if err := transfer.AddPeer(testEndpoint(2), int(PeerDHT)); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	// A candidate quarantined as a dead source.
	if err := transfer.AddPeer(testEndpoint(3), int(PeerDHT)); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	// A LowID source: it can never accept our call.
	if err := transfer.AddPeer(testEndpoint(4), int(PeerServer)); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	for i := range transfer.policy.peers {
		switch transfer.policy.peers[i].Endpoint {
		case testEndpoint(2):
			transfer.policy.peers[i].NextConnection = now + Seconds(30)
			transfer.policy.peers[i].FailCount = 2
		case testEndpoint(3):
			transfer.policy.peers[i].DeadUntil = now + Minutes(2)
			transfer.policy.peers[i].DeadStrikes = 3
		case testEndpoint(4):
			transfer.policy.peers[i].Connectable = false
		}
	}

	infos := transfer.GetPeersInfo()
	if len(infos) != 4 {
		t.Fatalf("source list has %d entries, want all 4 known sources", len(infos))
	}

	got := make(map[string]PeerInfo, len(infos))
	for _, info := range infos {
		got[info.Endpoint.String()] = info
	}
	if state := got[testEndpoint(1).String()].State; state != "connecting" {
		t.Fatalf("idle candidate state = %q, want connecting", state)
	}
	if info := got[testEndpoint(2).String()]; info.State != "waiting" || info.RetryIn <= 0 {
		t.Fatalf("backed-off candidate = %q retryIn=%d, want waiting with a countdown", info.State, info.RetryIn)
	}
	if info := got[testEndpoint(3).String()]; info.State != "dead" || info.DeadStrikes != 3 || info.RetryIn <= 0 {
		t.Fatalf("quarantined candidate = %q strikes=%d retryIn=%d", info.State, info.DeadStrikes, info.RetryIn)
	}
	if state := got[testEndpoint(4).String()].State; state != "unconnectable" {
		t.Fatalf("LowID candidate state = %q, want unconnectable", state)
	}

	// Ordering must read like eMule: actionable sources first, dead last.
	if infos[0].State != "connecting" {
		t.Fatalf("first source is %q, want the actionable candidate first", infos[0].State)
	}
	if last := infos[len(infos)-1].State; last != "dead" {
		t.Fatalf("last source is %q, want the dead source last", last)
	}
}

func TestIdleSourceStatePrecedence(t *testing.T) {
	now := CurrentTime()
	peer := NewPeerWithSource(testEndpoint(9), true, int(PeerServer))
	if state, retry := idleSourceState(now, peer); state != "connecting" || retry != 0 {
		t.Fatalf("fresh candidate = %q/%d, want connecting/0", state, retry)
	}
	peer.NextConnection = now + Seconds(20)
	if state, retry := idleSourceState(now, peer); state != "waiting" || retry <= 0 {
		t.Fatalf("cooling candidate = %q/%d, want waiting with retry", state, retry)
	}
	// Quarantine wins over the generic backoff: it is the stronger signal.
	peer.DeadUntil = now + Minutes(1)
	if state, _ := idleSourceState(now, peer); state != "dead" {
		t.Fatalf("quarantined candidate = %q, want dead", state)
	}
	// An unreachable (LowID) source is reported as such even when due.
	low := NewPeerWithSource(testEndpoint(8), false, int(PeerDHT))
	if state, _ := idleSourceState(now, low); state != "unconnectable" {
		t.Fatalf("LowID candidate = %q, want unconnectable", state)
	}
}
