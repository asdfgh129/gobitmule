package kad

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestEncryptDecryptDatagramRoundTrip(t *testing.T) {
	selfID := NewID(protocol.MustHashFromString("00112233445566778899AABBCCDDEEFF"))
	const selfUDPKey uint32 = 0x1A2B3C4D
	const peerUDPKey uint32 = 0x5A6B7C8D

	plain, err := (Hello{ID: selfID, TCPPort: 4662, Version: 8}).Pack(HelloReqOp)
	if err != nil {
		t.Fatalf("pack hello: %v", err)
	}
	encrypted, err := EncryptDatagram(plain, selfID, selfUDPKey, peerUDPKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(encrypted, plain) || encrypted[0] == ProtocolHeader {
		t.Fatal("encrypted datagram must not look like plaintext KAD")
	}
	got, senderKey, ok := DecryptDatagram(encrypted, selfID, selfUDPKey)
	if !ok {
		t.Fatal("decrypt with KadID key failed")
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("payload mismatch: %x != %x", got, plain)
	}
	if senderKey != peerUDPKey {
		t.Fatalf("sender key = %x, want %x", senderKey, peerUDPKey)
	}
}

func TestDecryptDatagramReceiverKeyFallback(t *testing.T) {
	selfID := NewID(protocol.MustHashFromString("00112233445566778899AABBCCDDEEFF"))
	const selfUDPKey uint32 = 0xDEADBEEF
	plain, err := (Req{SearchType: FindValue, Target: selfID, Receiver: selfID}).Pack()
	if err != nil {
		t.Fatalf("pack req: %v", err)
	}
	encrypted, err := EncryptDatagram(plain, ID{}, selfUDPKey, 0x12345678)
	if err != nil {
		t.Fatalf("encrypt with receiver key: %v", err)
	}
	got, _, ok := DecryptDatagram(encrypted, selfID, selfUDPKey)
	if !ok || !bytes.Equal(got, plain) {
		t.Fatalf("receiver-key decrypt failed: ok=%v got=%x", ok, got)
	}
}

func TestDecryptPlaintextPassThrough(t *testing.T) {
	plain := []byte{ProtocolHeader, PingOp}
	got, _, ok := DecryptDatagram(plain, NewID(protocol.MustHashFromString("00112233445566778899AABBCCDDEEFF")), 1)
	if ok || !bytes.Equal(got, plain) {
		t.Fatalf("plaintext must pass through: ok=%v got=%x", ok, got)
	}
}
