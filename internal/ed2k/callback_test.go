package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// #1: a low-ID source cannot be dialed directly; its token must be turned into
// a server callback request and remembered so a relayed endpoint can be matched.
func TestRequestCallbackForLowID(t *testing.T) {
	session := NewSession(NewSettings())
	hash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")

	// No connected server: request must be rejected without recording.
	if session.RequestCallbackForLowID(0x000A0001, hash) {
		t.Fatal("callback request accepted without a connected server")
	}
	if len(session.callbacks) != 0 {
		t.Fatal("callback recorded without a connected server")
	}

	// Attach a fake connected server and retry with a low ID.
	sc := NewServerConnection("fake", nil, session)
	sc.handshakeCompleted = true
	session.serverConnection = sc
	if !session.RequestCallbackForLowID(0x000A0001, hash) {
		t.Fatal("low-ID callback request rejected")
	}
	session.mu.Lock()
	got := session.callbacks[0x000A0001]
	session.mu.Unlock()
	if !got.Equal(hash) {
		t.Fatalf("callback hash=%s want %s", got.String(), hash.String())
	}

	// A high ID must never be callback-requested (it is directly dialable).
	if session.RequestCallbackForLowID(int32(0x75010001), hash) {
		t.Fatal("callback request accepted for a high ID")
	}
}

// HandleCallbackRequest offers the relayed endpoint to active transfers.
func TestHandleCallbackRequestAddsPeer(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(),
		Size:       1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	session.mu.Lock()
	session.transfers[transfer.GetHash()] = transfer
	session.mu.Unlock()
	endpoint := protocol.NewEndpoint(int32(0x75010001), 4662)
	if added := session.HandleCallbackRequest(endpoint); added != 1 {
		t.Fatalf("added=%d want 1", added)
	}
	if transfer.policy.Size() != 1 {
		t.Fatalf("transfer policy size=%d want 1", transfer.policy.Size())
	}
	// Low-ID endpoints must not be added.
	transfer.policy.ErasePeers()
	if added := session.HandleCallbackRequest(protocol.NewEndpoint(0x000A0001, 4662)); added != 0 {
		t.Fatalf("low-ID callback endpoint was added: %d", added)
	}
}
