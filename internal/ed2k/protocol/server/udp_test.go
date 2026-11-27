package server

import (
	"bytes"
	"encoding/hex"
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

// A real OP_GLOBFOUNDSOURCES answer captured from a public eD2k server
// (57.128.161.24:6666, "Cyberchrist" 18.1) while our client asked for sources
// of D6DA273629B1C6781E96E0BAB4142602 with OP_GLOBGETSOURCES2. Framing is
// <E3 9B><hash 16><count 1><ip 4><port 2>*count, byte order per eMule's
// CPartFile::AddSources + ipstr() (first octet in the low byte).
func TestParseUDPFoundSourcesRealServerAnswer(t *testing.T) {
	raw, err := hex.DecodeString("e39bd6da273629b1c6781e96e0bab414260202041a01003612ffc005005a88")
	if err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	got := ParseUDPFoundSources(raw)
	if len(got) != 1 {
		t.Fatalf("hashes = %d, want 1", len(got))
	}
	hash := protocol.MustHashFromString("D6DA273629B1C6781E96E0BAB4142602")
	endpoints := got[hash]
	if len(endpoints) != 2 {
		t.Fatalf("sources = %d, want 2", len(endpoints))
	}
	if got, want := endpoints[0].String(), "4.26.1.0:4662"; got != want {
		t.Fatalf("source[0] = %s, want %s", got, want)
	}
	if got, want := endpoints[1].String(), "255.192.5.0:34906"; got != want {
		t.Fatalf("source[1] = %s, want %s", got, want)
	}
	// Both entries are low IDs (first octet encoded below 0x01000000), which is
	// why the session dropped them instead of queueing a direct dial.
	for _, endpoint := range endpoints {
		if endpoint.IP() >= 0x01000000 {
			t.Fatalf("%s expected to be a low ID", endpoint.String())
		}
	}
}
