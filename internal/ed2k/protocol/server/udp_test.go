package server

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func TestUDPFoundSourcesRoundTrip(t *testing.T) {
	h := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	datagram := UDPGlobGetSources(h, 123456)
	if len(datagram) < 3 || datagram[0] != protocol.EdonkeyHeader || datagram[1] != OpGlobGetSources2 {
		t.Fatalf("bad glob get sources datagram: % X", datagram[:2])
	}
	// Build a synthetic answer: hash + count + (ip,port).
	var body bytes.Buffer
	_ = protocol.WriteHash(&body, h)
	_ = body.WriteByte(2)
	_ = protocol.WriteInt32(&body, 0x75010001)
	_ = protocol.WriteUInt16(&body, 4662)
	_ = protocol.WriteInt32(&body, 0x0A000001)
	_ = protocol.WriteUInt16(&body, 4672)
	answer := packUDPDatagram(OpGlobFoundSources, body.Bytes())
	got := ParseUDPFoundSources(answer)
	endpoints := got[h]
	if len(endpoints) != 2 {
		t.Fatalf("endpoints=%d want 2", len(endpoints))
	}
	if endpoints[0].IP() != int32(0x75010001) || endpoints[0].Port() != 4662 {
		t.Fatalf("endpoint0=%s", endpoints[0].String())
	}
}

func TestUDPServerEncryptionRoundTrip(t *testing.T) {
	plain := packUDPDatagram(OpGlobGetSources2, bytes.Repeat([]byte{0xAB}, 24))
	const key = uint32(0x1234ABCD)
	wire, err := EncryptServerDatagram(plain, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Equal(wire, plain) {
		t.Fatal("encrypted datagram equals plaintext")
	}
	got, ok := DecryptServerDatagram(wire, key)
	if !ok {
		t.Fatal("decrypt failed")
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypted differs: got % X want % X", got, plain)
	}
	// Wrong key must not claim success.
	if _, ok := DecryptServerDatagram(wire, key^0x1); ok {
		t.Fatal("decrypt succeeded with wrong key")
	}
	// Plaintext passes through untouched.
	if out, ok := DecryptServerDatagram(plain, key); ok || !bytes.Equal(out, plain) {
		t.Fatal("plaintext datagram not passed through")
	}
}
