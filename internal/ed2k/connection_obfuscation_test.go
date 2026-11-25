package goed2k

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol/obfuscation"
)

// TestConnectionIncomingObfuscationAcceptsPlaintext verifies that enabling
// obfuscation on the receive path does not break cleartext peers — a core
// compatibility requirement for ED-05 (opt-in must not regress ED-01..04).
func TestConnectionIncomingObfuscationAcceptsPlaintext(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	conn := NewConnection(nil)
	conn.socket = server

	plain := append([]byte{protocol.EMuleProt}, []byte("plain framed data")...)
	go func() {
		_, _ = client.Write(plain)
	}()

	greeting, err := conn.enableIncomingObfuscation(protocol.MustHashFromString("22222222222222222222222222222222"))
	if err != nil {
		t.Fatalf("enableIncomingObfuscation: %v", err)
	}
	if greeting {
		t.Fatal("plaintext peer was misclassified as a silent server probe")
	}
	if conn.IsObfuscated() {
		t.Fatal("plaintext peer wrongly marked as obfuscated")
	}

	buf := make([]byte, len(plain))
	if _, err := io.ReadFull(conn.socket, buf); err != nil {
		t.Fatalf("read plaintext: %v", err)
	}
	if string(buf) != string(plain) {
		t.Fatalf("plaintext mismatch: got %q want %q", buf, plain)
	}
}

// TestConnectionIncomingObfuscationCompletesHandshake verifies the receive
// path upgrades a real obfuscated peer into an encrypted net.Conn.
func TestConnectionIncomingObfuscationCompletesHandshake(t *testing.T) {
	peerHash := protocol.MustHashFromString("22222222222222222222222222222222") // B == A target
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	conn := NewConnection(nil)
	conn.socket = server

	done := make(chan error, 1)
	go func() {
		c, err := obfuscation.WrapOutgoing(client, peerHash, []byte{0x09})
		if err == nil {
			_, err = c.Write([]byte("encrypted request"))
		}
		done <- err
	}()

	greeting, err := conn.enableIncomingObfuscation(peerHash)
	if err != nil {
		t.Fatalf("enableIncomingObfuscation: %v", err)
	}
	if greeting {
		t.Fatal("obfuscated peer was misclassified as a silent server probe")
	}
	if !conn.IsObfuscated() {
		t.Fatal("obfuscated peer not marked as obfuscated")
	}

	// Read before waiting on the writer: net.Pipe is unbuffered, so the
	// writer blocks until we drain the decrypted payload.
	buf := make([]byte, len("encrypted request"))
	if _, err := io.ReadFull(conn.socket, buf); err != nil {
		t.Fatalf("read encrypted payload: %v", err)
	}
	if string(buf) != "encrypted request" {
		t.Fatalf("decrypted payload mismatch: got %q", buf)
	}
	if err := <-done; err != nil {
		t.Fatalf("outgoing writer: %v", err)
	}
}

// TestConnectionPeerHashState checks the outgoing-obfuscation opt-in gate:
// an invalid/zero hash leaves plaintext mode, a valid hash arms the handshake.
func TestConnectionPeerHashState(t *testing.T) {
	conn := NewConnection(nil)
	if !conn.obfPeerHash.Equal(protocol.Invalid) {
		t.Fatal("fresh connection should not have outgoing obfuscation armed")
	}
	valid := protocol.MustHashFromString("ABCDEF0123456789ABCDEF0123456789")
	conn.SetObfuscationPeerHash(valid)
	if conn.obfPeerHash.Equal(protocol.Invalid) || conn.obfPeerHash.String() != valid.String() {
		t.Fatal("SetObfuscationPeerHash did not arm the target hash")
	}
	conn.SetObfuscationPeerHash(protocol.Invalid)
	if !conn.obfPeerHash.Equal(protocol.Invalid) {
		t.Fatal("invalid hash should disarm outgoing obfuscation")
	}
}

// TestIncomingSilentProbeGetsHello reproduces the eD2k server callback test:
// the server connects to our listening port and waits for the client to send
// the first Hello frame. Previously WrapIncoming blocked reading a marker and
// closed the socket, so the server assigned a LowID.
func TestIncomingPrefixKeepsPartialHeader(t *testing.T) {
	conn := NewConnection(nil)
	prefix := []byte{protocol.EdonkeyHeader, 0x3B, 0x00, 0x00, 0x00}
	conn.AppendIncoming(prefix)

	headers, bodies, err := conn.ReadFramesWithCombiner(nil)
	if err != nil {
		t.Fatalf("ReadFramesWithCombiner: %v", err)
	}
	if len(headers) != 0 || len(bodies) != 0 {
		t.Fatalf("partial header yielded %d frames", len(headers))
	}
	if got := string(conn.DrainIncoming()); got != string(prefix) {
		t.Fatalf("partial header lost: got %X want %X", got, prefix)
	}
}

func TestIncomingSilentProbeGetsHello(t *testing.T) {
	oldWait := incomingProbeWait
	incomingProbeWait = 20 * time.Millisecond
	t.Cleanup(func() { incomingProbeWait = oldWait })

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	session := NewSession(NewSettings())
	pc := NewIncomingPeerConnection(session, server)
	greeting, err := pc.enableIncomingObfuscation(session.GetUserAgent())
	if err != nil {
		t.Fatalf("enableIncomingObfuscation: %v", err)
	}
	if !greeting {
		t.Fatal("silent server probe was not detected")
	}
	if pc.IsObfuscated() {
		t.Fatal("silent probe must stay plaintext")
	}

	pc.OnConnect()
	packets := pc.PendingPackets()
	if len(packets) != 1 {
		t.Fatalf("queued packets = %d, want 1", len(packets))
	}
	raw := packets[0]
	if len(raw) < 6 || raw[0] != protocol.EdonkeyHeader || raw[5] != 0x01 {
		t.Fatalf("silent probe did not queue a valid Hello frame: %X", raw)
	}
}
