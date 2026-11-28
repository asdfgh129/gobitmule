package server

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// These helpers re-implement the server datagram envelope directly from
// eMule's EncryptedDatagramSocket.cpp (EncryptSendServer / DecryptReceivedServer)
// and otherfunctions.cpp (RC4CreateKey / RC4Crypt). They deliberately do NOT
// call EncryptServerDatagram / DecryptServerDatagram, so a shared mistake in our
// production code cannot make the cross-check pass.

const (
	emuleMagicUDPsyncServer  = uint32(0x13EF24D5)
	emuleMagicServerClient   = byte(0xA5)
	emuleMagicClientServer   = byte(0x6B)
	emuleCryptHeaderNoPad    = 8
)

// emuleRC4Key mirrors:
//
//	MD5(<baseKey 4 little endian><direction 1><randomPart 2>)
//	RC4CreateKey(md5, 16, &key, true)
//
// The last argument is bSkipDiscard: TRUE means SKIP the 1024-byte discard
// step (otherfunctions.cpp: "if (!bSkipDiscard) RC4Crypt(NULL, NULL, 1024, key)").
// Go's crypto/rc4 starts emitting keystream immediately, which is equivalent.
func emuleRC4Key(t *testing.T, baseKey uint32, direction byte, randomPart []byte) *rc4.Cipher {
	t.Helper()
	if len(randomPart) != 2 {
		t.Fatalf("randomPart = %d bytes", len(randomPart))
	}
	keyData := make([]byte, 7)
	binary.LittleEndian.PutUint32(keyData[:4], baseKey)
	keyData[4] = direction
	copy(keyData[5:], randomPart)
	sum := md5.Sum(keyData)
	cipher, err := rc4.NewCipher(sum[:])
	if err != nil {
		t.Fatalf("rc4: %v", err)
	}
	return cipher
}

// emuleWrapDatagram builds a datagram exactly as an eMule server would build
// its answer to us: direction MAGICVALUE_UDP_SERVERCLIENT.
func emuleWrapDatagram(t *testing.T, plain []byte, baseKey uint32, randomPart []byte, padLen byte) []byte {
	t.Helper()
	cipher := emuleRC4Key(t, baseKey, emuleMagicServerClient, randomPart)
	padLen &= 0x0f
	scratch := make([]byte, 4+1+int(padLen)+len(plain))
	binary.LittleEndian.PutUint32(scratch[:4], emuleMagicUDPsyncServer)
	scratch[4] = padLen
	for i := 0; i < int(padLen); i++ {
		scratch[5+i] = byte(0xC0 + i)
	}
	copy(scratch[5+int(padLen):], plain)
	out := make([]byte, 3+len(scratch))
	out[0] = 0x42 // semi-random marker, never an eD2k protocol header
	copy(out[1:3], randomPart)
	cipher.XORKeyStream(out[3:], scratch)
	return out
}

func emuleUnwrapDatagram(t *testing.T, wire []byte, baseKey uint32, direction byte) ([]byte, bool) {
	t.Helper()
	if len(wire) <= emuleCryptHeaderNoPad {
		return nil, false
	}
	cipher := emuleRC4Key(t, baseKey, direction, wire[1:3])
	dec := make([]byte, len(wire)-3)
	cipher.XORKeyStream(dec, wire[3:])
	if len(dec) < 5 || binary.LittleEndian.Uint32(dec[:4]) != emuleMagicUDPsyncServer {
		return nil, false
	}
	pad := int(dec[4] & 0x0f)
	if len(dec) <= pad {
		return nil, false
	}
	return dec[5+pad:], true
}

// Our receiver must decode a real eMule-shaped server answer, including the
// padding case eMule permits even though it currently sends padLen == 0.
func TestDecryptServerDatagramAgainstEMuleServerVector(t *testing.T) {
	plain := packUDPDatagram(OpGlobFoundSources, bytes.Repeat([]byte{0x5A}, 24))
	const baseKey = uint32(0xDEADBEEF)
	cases := []struct {
		name       string
		randomPart [2]byte
		padLen     byte
	}{
		{"no padding", [2]byte{0x12, 0x34}, 0},
		{"5 bytes padding", [2]byte{0xFF, 0x01}, 5},
		{"15 bytes padding", [2]byte{0x00, 0x80}, 15},
	}
	for _, tc := range cases {
		wire := emuleWrapDatagram(t, plain, baseKey, tc.randomPart[:], tc.padLen)
		got, ok := DecryptServerDatagram(wire, baseKey)
		if !ok {
			t.Fatalf("%s: our decoder rejected an eMule server datagram", tc.name)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("%s: got % X, want % X", tc.name, got, plain)
		}
	}
}

// Our outgoing envelope must be readable by an unmodified eMule server. The
// independent decoder above only tries MAGICVALUE_UDP_CLIENTSERVER, so a wrong
// direction byte or key derivation would fail here.
func TestEncryptServerDatagramMatchesEMuleDecoder(t *testing.T) {
	plain := packUDPDatagram(OpGlobGetSources2, append(bytes.Repeat([]byte{0x77}, 16), 1, 2, 3, 4))
	const baseKey = uint32(0x00C0FFEE)
	wire, err := EncryptServerDatagram(plain, baseKey)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, ok := emuleUnwrapDatagram(t, wire, baseKey, emuleMagicClientServer)
	if !ok {
		t.Fatal("an unmodified eMule server could not decode our datagram")
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("eMule decoder got % X, want % X", got, plain)
	}
	if wire[0] == protocol.EdonkeyHeader {
		t.Fatal("envelope marker collides with an eD2k protocol header")
	}
	// eMule's DecryptReceivedServer tries only SERVERCLIENT for inbound data,
	// so a client->server envelope must not decode as a server answer.
	if _, ok := emuleUnwrapDatagram(t, wire, baseKey, emuleMagicServerClient); ok {
		t.Fatal("client->server envelope decoded with the server->client direction")
	}
}

// A wrong key or an overlong pad length must be rejected rather than yielding
// garbage that a parser could mistake for a packet.
func TestDecryptServerDatagramRejectsInvalidEnvelope(t *testing.T) {
	plain := packUDPDatagram(OpGlobGetSources2, bytes.Repeat([]byte{0x11}, 20))
	const baseKey = uint32(0x1A2B3C4D)
	randomPart := []byte{0xAA, 0xBB}
	wire := emuleWrapDatagram(t, plain, baseKey, randomPart, 0)
	if _, ok := DecryptServerDatagram(wire, baseKey^1); ok {
		t.Fatal("wrong base key accepted")
	}
	// Truncate the encrypted body so the declared padding cannot fit.
	short := wire[:emuleCryptHeaderNoPad]
	if _, ok := DecryptServerDatagram(short, baseKey); ok {
		t.Fatal("truncated envelope accepted")
	}
	// A plaintext eD2k datagram must pass through untouched with ok=false.
	if out, ok := DecryptServerDatagram(plain, baseKey); ok || !bytes.Equal(out, plain) {
		t.Fatal("plaintext datagram was modified")
	}
}
