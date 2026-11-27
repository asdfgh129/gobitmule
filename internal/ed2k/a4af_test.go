package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// #3: a source that does not hold file A is offered to other downloads (B)
// instead of being thrown away. The source of file A itself is not duplicated.
func TestA4AFReassignToOtherTransfer(t *testing.T) {
	session := NewSession(NewSettings())
	hashA := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	hashB := protocol.MustHashFromString("C7A91B4E8D2F3A0B5E6D7C8091A2B3C4")
	for _, h := range []protocol.Hash{hashA, hashB} {
		tr, err := NewTransfer(session, AddTransferParams{
			Hash: h, CreateTime: CurrentTime(), Size: 2 * 1024 * 1024,
		})
		if err != nil {
			t.Fatalf("NewTransfer: %v", err)
		}
		session.mu.Lock()
		session.transfers[h] = tr
		session.mu.Unlock()
	}
	endpoint := protocol.NewEndpoint(int32(0x75010001), 4662)
	if got := session.A4AFReassign(endpoint, hashA); got != 1 {
		t.Fatalf("reassigned=%d want 1 (only transfer B)", got)
	}
	trB := session.transfers[hashB]
	if trB.policy.Get(endpoint) == nil {
		t.Fatal("transfer B did not receive the reassigned source")
	}
	if session.transfers[hashA].policy.Get(endpoint) != nil {
		t.Fatal("source was duplicated back into transfer A")
	}
}
