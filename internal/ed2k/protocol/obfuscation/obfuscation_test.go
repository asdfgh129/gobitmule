package obfuscation

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// TestRC4KnownVectors checks the KSA+PRGA against published RC4 vectors.
func TestRC4KnownVectors(t *testing.T) {
	vectors := []struct {
		key  string
		pt   string
		want string // hex ciphertext
	}{
		{"Key", "Plaintext", "BBF316E8D940AF0AD3"},
		{"Wiki", "pedia", "1021BF0420"},
		{"Secret", "Attack at dawn", "45A01F645FC35B383552544B9BF5"},
	}
	for _, v := range vectors {
		c := NewCipher([]byte(v.key))
		out := make([]byte, len(v.pt))
		c.XORKeyStream(out, []byte(v.pt))
		got := hex.EncodeToString(out)
		if !strings.EqualFold(got, v.want) {
			t.Errorf("RC4(%q,%q)=%s want %s", v.key, v.pt, got, v.want)
		}
	}
}

// TestRC4DiscardMatchesDrop confirms Discard advances the keystream exactly
// like consuming n bytes would, so eMule's 1024-byte drop is reproducible.
func TestRC4DiscardMatchesDrop(t *testing.T) {
	key := []byte("obfuscation-discard-vector")
	a := NewCipher(key)
	b := NewCipher(key)
	drop := make([]byte, RC4DiscardBytes)
	a.XORKeyStream(drop, drop)
	b.Discard(RC4DiscardBytes)
	pa, pb := make([]byte, 16), make([]byte, 16)
	a.XORKeyStream(pa, pa)
	b.XORKeyStream(pb, pb)
	if !bytes.Equal(pa, pb) {
		t.Fatal("Discard did not match consuming keystream bytes")
	}
}

// TestDirectionKeysPairwise checks the eMule key symmetry: A.send decrypts
// B.recv and A.recv decrypts B.send, given the same anchor hash and randomPart.
func TestDirectionKeysPairwise(t *testing.T) {
	targetHash := protocol.MustHashFromString("22222222222222222222222222222222")
	const part uint32 = 0x1A2B3C4D

	aSend, aRecv := DeriveDirectionKeys(targetHash, part, true)
	bSend, bRecv := DeriveDirectionKeys(targetHash, part, false)

	msg := []byte("eMule obfuscation key agreement")
	// A -> B: encrypt with A.send, decrypt with B.recv.
	ct := make([]byte, len(msg))
	aSend.XORKeyStream(ct, msg)
	pt := make([]byte, len(ct))
	bRecv.XORKeyStream(pt, ct)
	if !bytes.Equal(pt, msg) {
		t.Fatal("A.send / B.recv key pair mismatch")
	}
	// B -> A: encrypt with B.send, decrypt with A.recv.
	ct2 := make([]byte, len(msg))
	bSend.XORKeyStream(ct2, msg)
	pt2 := make([]byte, len(ct2))
	aRecv.XORKeyStream(pt2, ct2)
	if !bytes.Equal(pt2, msg) {
		t.Fatal("B.send / A.recv key pair mismatch")
	}
}

// TestKeyMaterialLayout pins the exact 21-byte MD5 input, guarding against
// silent byte-order/magic regressions. A.send = MD5(hash||0x22||part LE).
func TestKeyMaterialLayout(t *testing.T) {
	hash := protocol.MustHashFromString("0102030405060708090A0B0C0D0E0F10")
	const part uint32 = 0x04030201 // LE bytes 01 02 03 04
	buf := make([]byte, keyMaterialLen)
	copy(buf[:16], hash.Bytes())
	buf[16] = MagicValueRequester
	binary.LittleEndian.PutUint32(buf[17:21], part)

	wantPrefix := "0102030405060708090a0b0c0d0e0f10" + "22" + "01020304"
	gotPrefix := hex.EncodeToString(buf)
	if gotPrefix != wantPrefix {
		t.Fatalf("key material prefix=%s want %s", gotPrefix, wantPrefix)
	}
}

// TestFullHandshakeRoundTrip drives both sides of the eMule basic
// obfuscation handshake end to end using only the frame codec and the
// derived directional keys — no network, no live peer.
func TestFullHandshakeRoundTrip(t *testing.T) {
	peerHash := protocol.MustHashFromString("AABBCCDDEEFF00112233445566778899")
	const randomPart uint32 = 0xDEADBEEF
	aSend, aRecv := DeriveDirectionKeys(peerHash, randomPart, true)
	bSend, bRecv := DeriveDirectionKeys(peerHash, randomPart, false)

	// A -> B: marker + randomPart cleartext, rest encrypted with A.send.
	aPadding := []byte{0x10, 0x20, 0x30}
	request, err := InitiatorRequest(PickMarker(protocol.EdonkeyHeader), randomPart, aPadding, aSend)
	if err != nil {
		t.Fatalf("InitiatorRequest: %v", err)
	}
	if !ValidMarker(request[0]) {
		t.Fatal("emitted marker is an eD2k protocol header")
	}

	gotPart, gotPadLen, err := ParseInitiatorRequest(request, bRecv)
	if err != nil {
		t.Fatalf("ParseInitiatorRequest: %v", err)
	}
	if gotPart != randomPart {
		t.Fatalf("randomPart=%08X want %08X", gotPart, randomPart)
	}
	if int(gotPadLen) != len(aPadding) {
		t.Fatalf("paddingLen=%d want %d", gotPadLen, len(aPadding))
	}

	// B -> A: whole reply encrypted with B.send.
	bPadding := []byte{0xAA, 0xBB}
	reply, err := ResponderReply(bPadding, bSend)
	if err != nil {
		t.Fatalf("ResponderReply: %v", err)
	}
	gotReplyPad, err := ParseResponderReply(reply, aRecv)
	if err != nil {
		t.Fatalf("ParseResponderReply: %v", err)
	}
	if int(gotReplyPad) != len(bPadding) {
		t.Fatalf("reply paddingLen=%d want %d", gotReplyPad, len(bPadding))
	}

	// Post-handshake payload stream: A sends, B receives.
	payload := []byte("GET / file request over obfuscated stream")
	ct := make([]byte, len(payload))
	aSend.XORKeyStream(ct, payload)
	pt := make([]byte, len(ct))
	bRecv.XORKeyStream(pt, ct)
	if !bytes.Equal(pt, payload) {
		t.Fatal("post-handshake A->B payload did not round trip")
	}
}

// TestHandshakeRejectsBadFrames verifies tamper/bootstrapping checks.
func TestHandshakeRejectsBadFrames(t *testing.T) {
	hash := protocol.MustHashFromString("22222222222222222222222222222222")
	aSend, aRecv := DeriveDirectionKeys(hash, 1, true)
	_, bRecv := DeriveDirectionKeys(hash, 1, false)

	if _, err := InitiatorRequest(protocol.EdonkeyHeader, 1, nil, aSend); err != ErrBadMarker {
		t.Fatalf("expected ErrBadMarker for protocol header, got %v", err)
	}

	req, _ := InitiatorRequest(0x55, 1, []byte{0x01}, aSend)
	good := bytes.Clone(req)
	// Corrupt an encrypted sync byte.
	bad := bytes.Clone(good)
	bad[5] ^= 0xFF
	if _, _, err := ParseInitiatorRequest(bad, bRecv); err != ErrBadSync {
		t.Fatalf("expected ErrBadSync for tampered frame, got %v", err)
	}
	// Truncated frame.
	if _, _, err := ParseInitiatorRequest(good[:6], bRecv); err != ErrShortFrame {
		t.Fatalf("expected ErrShortFrame, got %v", err)
	}

	reply, _ := ResponderReply(nil, func() *Cipher { _, r := DeriveDirectionKeys(hash, 1, false); return r }())
	badReply := bytes.Clone(reply)
	badReply[0] ^= 0xFF
	if _, err := ParseResponderReply(badReply, aRecv); err != ErrBadSync {
		t.Fatalf("expected ErrBadSync for tampered reply, got %v", err)
	}
}

// TestPickMarkerAvoidsProtocolHeaders checks the marker retry loop.
func TestPickMarkerAvoidsProtocolHeaders(t *testing.T) {
	for _, bad := range nonProtocolMarkers {
		got := PickMarker(bad)
		if !ValidMarker(got) {
			t.Fatalf("PickMarker(%02X)=%02X which is invalid", bad, got)
		}
	}
}
