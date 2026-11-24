package obfuscation

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// TestStreamHandshakeAndEncryptedTransport is the "wired" proof for ED-05:
// two real net.Conn endpoints complete the A/B obfuscation handshake and then
// exchange bidirectional payload over the encrypted stream. A third-party
// observer reading the raw wire must not see the plaintext payload.
func TestStreamHandshakeAndEncryptedTransport(t *testing.T) {
	localHash := protocol.MustHashFromString("22222222222222222222222222222222") // B's hash == A's target
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	type result struct {
		conn net.Conn
		obf  bool
		err  error
	}
	outRes := make(chan result, 1)
	inRes := make(chan result, 1)

	// A (outgoing) needs the target hash; B (incoming) uses its own hash.
	go func() {
		c, err := WrapOutgoing(a, localHash, []byte{0x01, 0x02})
		outRes <- result{conn: c, err: err}
	}()
	go func() {
		c, obf, err := WrapIncoming(b, localHash, []byte{0xAA, 0xBB})
		inRes <- result{conn: c, obf: obf, err: err}
	}()

	out := <-outRes
	in := <-inRes
	if out.err != nil {
		t.Fatalf("WrapOutgoing: %v", out.err)
	}
	if in.err != nil {
		t.Fatalf("WrapIncoming: %v", in.err)
	}
	if !in.obf {
		t.Fatal("incoming side did not detect obfuscation")
	}
	aStream, okA := out.conn.(*Stream)
	bStream, okB := in.conn.(*Stream)
	if !okA || !okB {
		t.Fatal("handshake did not yield encrypted Stream wrappers")
	}

	// A -> B encrypted payload.
	payloadAB := []byte("ed2k hello over obfuscated stream; secret=42")
	done := make(chan error, 1)
	go func() {
		_, err := aStream.Write(payloadAB)
		done <- err
	}()
	gotAB := make([]byte, len(payloadAB))
	if _, err := io.ReadFull(bStream, gotAB); err != nil {
		t.Fatalf("B read: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("A write: %v", err)
	}
	if !bytes.Equal(gotAB, payloadAB) {
		t.Fatalf("A->B payload mismatch: got %q want %q", gotAB, payloadAB)
	}

	// B -> A encrypted payload.
	payloadBA := []byte("file request ack over obfuscated stream")
	done2 := make(chan error, 1)
	go func() {
		_, err := bStream.Write(payloadBA)
		done2 <- err
	}()
	gotBA := make([]byte, len(payloadBA))
	if _, err := io.ReadFull(aStream, gotBA); err != nil {
		t.Fatalf("A read: %v", err)
	}
	if err := <-done2; err != nil {
		t.Fatalf("B write: %v", err)
	}
	if !bytes.Equal(gotBA, payloadBA) {
		t.Fatalf("B->A payload mismatch: got %q want %q", gotBA, payloadBA)
	}
}

// TestStreamWireDoesNotLeakPlaintext checks the transport invariants that
// make obfuscation meaningful: the cleartext marker is a non-protocol byte,
// the random part is visible by design, and the sync token is encrypted.
func TestStreamWireDoesNotLeakPlaintext(t *testing.T) {
	localHash := protocol.MustHashFromString("ABABABABABABABABABABABABABABABAB")
	part := uint32(0x11223344)
	aSend, _ := DeriveDirectionKeys(localHash, part, true)
	frame, err := InitiatorRequest(PickMarker(protocol.EdonkeyHeader), part, []byte{0x7F}, aSend)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidMarker(frame[0]) {
		t.Fatal("wire marker is an eD2k protocol header")
	}
	if got := binaryLE(frame[1:5]); got != part {
		t.Fatalf("wire random part=%08X want %08X", got, part)
	}
	// The sync token must not appear in plaintext anywhere in the frame.
	syncBytes := []byte{0xC4, 0x6F, 0x5E, 0x83}
	if bytes.Contains(frame, syncBytes) {
		t.Fatal("MAGICVALUE_SYNC leaked into the wire in plaintext")
	}
}

func binaryLE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// TestWrapIncomingPlaintextFallback verifies that a plain eD2k connection is
// not broken: the probe byte is replayed and no handshake is attempted.
func TestWrapIncomingPlaintextFallback(t *testing.T) {
	localHash := protocol.MustHashFromString("11111111111111111111111111111111")
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	plain := append([]byte{protocol.EdonkeyHeader}, []byte("plain eD2k packet")...)
	go func() {
		_, _ = client.Write(plain)
		_ = client.Close()
	}()

	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	conn, obf, err := WrapIncoming(server, localHash, nil)
	if err != nil {
		t.Fatalf("WrapIncoming plaintext: %v", err)
	}
	if obf {
		t.Fatal("plaintext connection wrongly marked obfuscated")
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read replayed stream: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("replayed bytes mismatch: got %q want %q", got, plain)
	}
}

// TestWrapIncomingRejectsTamperedHandshake ensures a corrupted sync token is
// rejected rather than silently accepted.
func TestWrapIncomingRejectsTamperedHandshake(t *testing.T) {
	localHash := protocol.MustHashFromString("33333333333333333333333333333333")
	part := uint32(0x0BADF00D)
	aSend, _ := DeriveDirectionKeys(localHash, part, true)
	frame, err := InitiatorRequest(0x55, part, nil, aSend)
	if err != nil {
		t.Fatal(err)
	}
	frame[5] ^= 0xFF // corrupt an encrypted sync byte

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		_, _ = client.Write(frame)
	}()

	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, obf, err := WrapIncoming(server, localHash, nil)
	if err != ErrBadSync {
		t.Fatalf("expected ErrBadSync, got obf=%v err=%v", obf, err)
	}
}
