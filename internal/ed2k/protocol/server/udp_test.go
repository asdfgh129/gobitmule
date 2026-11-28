package server

import (
	"bytes"
	"encoding/binary"
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

// OP_GLOBSERVSTATRES is the second key source: eMule reads the server UDP key
// from body[36:40] only when the body is at least 40 bytes long.
func TestParseGlobServStatRes(t *testing.T) {
	body := make([]byte, 40)
	binary.LittleEndian.PutUint32(body[0:4], 0x12345678)  // challenge
	binary.LittleEndian.PutUint32(body[4:8], 51241)       // users
	binary.LittleEndian.PutUint32(body[8:12], 998877)     // files
	binary.LittleEndian.PutUint32(body[24:28], 0x73b)     // UDP flags
	binary.LittleEndian.PutUint16(body[32:34], 4674)      // UDP obfuscation port
	binary.LittleEndian.PutUint16(body[34:36], 4670)      // TCP obfuscation port
	binary.LittleEndian.PutUint32(body[36:40], 0xA1B2C3D4) // UDP key

	datagram := append([]byte{protocol.EdonkeyHeader, OpGlobServStatRes}, body...)
	res, ok := ParseGlobServStatRes(datagram)
	if !ok {
		t.Fatal("valid OP_GLOBSERVSTATRES was rejected")
	}
	if res.Challenge != 0x12345678 || res.UDPKey != 0xA1B2C3D4 {
		t.Fatalf("challenge/key = %#x/%#x", res.Challenge, res.UDPKey)
	}
	if res.Users != 51241 || res.Files != 998877 || res.UDPFlags != 0x73b {
		t.Fatalf("users/files/flags = %d/%d/%#x", res.Users, res.Files, res.UDPFlags)
	}
	if res.UDPObfuscationPort != 4674 || res.TCPObfuscationPort != 4670 {
		t.Fatalf("obfuscation ports = %d/%d", res.UDPObfuscationPort, res.TCPObfuscationPort)
	}

	// No key in a short answer: eMule leaves the previous key untouched.
	short := append([]byte{protocol.EdonkeyHeader, OpGlobServStatRes}, body[:36]...)
	res, ok = ParseGlobServStatRes(short)
	if !ok || res.UDPKey != 0 {
		t.Fatalf("short answer = %#x / ok=%t, want key 0", res.UDPKey, ok)
	}
	// Wrong opcode must not be mistaken for a status answer.
	if _, ok := ParseGlobServStatRes([]byte{protocol.EdonkeyHeader, OpGlobFoundSources, 1, 2, 3, 4}); ok {
		t.Fatal("0x9B accepted as OP_GLOBSERVSTATRES")
	}
}
