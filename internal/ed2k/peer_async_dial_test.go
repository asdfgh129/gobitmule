package goed2k

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// waitForDial pumps the async dial until it settles. The session loop does the
// same thing once per tick, outside of any blocking call.
func waitForDial(t *testing.T, pc *PeerConnection) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for pc.pumpDial() {
		if time.Now().After(deadline) {
			t.Fatal("async dial did not settle in 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A source dial used to run inline in the session loop with no timeout, so one
// unreachable candidate froze PumpIO - and every live peer's handshake and
// download - until the OS gave up on the SYN. ConnectAsync must hand the socket
// back through the pump instead of blocking the caller.
func TestConnectAsyncAdoptsSocketOnPump(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		close(accepted)
		// Hold the socket open so the connection stays usable.
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}()

	session := NewSession(NewSettings())
	endpoint := protocol.EndpointFromInet(ln.Addr().(*net.TCPAddr))
	pc := NewPeerConnection(session, endpoint, nil, nil)

	if err := pc.ConnectAsync(); err != nil {
		t.Fatalf("ConnectAsync: %v", err)
	}
	if pc.socket != nil {
		t.Fatal("ConnectAsync installed the socket on the caller goroutine")
	}

	waitForDial(t, pc)
	if pc.Dialing() {
		t.Fatal("dial flag still set after pump")
	}
	if pc.socket == nil {
		t.Fatalf("socket was not adopted, disconnect code=%d", pc.DisconnectCode())
	}
	if len(pc.outgoing) == 0 {
		t.Fatal("OnConnect did not queue the client Hello after adoption")
	}
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted the dial")
	}
	_ = pc.socket.Close()
}

// A failed dial must surface through the pump as a normal IOException close so
// the peer policy applies its usual backoff instead of retrying immediately.
func TestConnectAsyncFailureClosesOnPump(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close() // nothing is listening any more

	session := NewSession(NewSettings())
	pc := NewPeerConnection(session, protocol.EndpointFromInet(addr), nil, nil)
	if err := pc.ConnectAsync(); err != nil {
		t.Fatalf("ConnectAsync: %v", err)
	}
	waitForDial(t, pc)

	if !pc.IsDisconnecting() {
		t.Fatal("failed dial left the connection open")
	}
	if code := pc.DisconnectCode(); code != IOException {
		t.Fatalf("disconnect code = %d, want IOException(%d)", code, IOException)
	}
}

// The dial timeout must stay bounded: PumpIO serves every peer from one loop.
func TestDialTimeoutIsBounded(t *testing.T) {
	session := NewSession(NewSettings())
	c := NewConnection(session)
	got := c.dialTimeout()
	if got <= 0 {
		t.Fatalf("dial timeout = %v, want a positive bound", got)
	}
	if got > 30*time.Second {
		t.Fatalf("dial timeout = %v, too long to run inside the session loop", got)
	}

	// Without a session (unit-test paths) the package default must apply.
	bare := NewConnection(nil)
	if bare.dialTimeout() <= 0 {
		t.Fatal("session-less connection has no dial bound")
	}
	if err := NewError(EncryptionError); errorCodeOf(err) != EncryptionError {
		t.Fatal("errorCodeOf did not preserve the eD2k error code")
	}
	if code := errorCodeOf(errors.New("plain")); code != IOException {
		t.Fatalf("plain error mapped to %d, want IOException", code)
	}
}
