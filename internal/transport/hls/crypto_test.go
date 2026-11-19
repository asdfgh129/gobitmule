package hlstransport

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"
)

// encryptLikeCDN produces a real AES-128 CBC + PKCS7 segment, the way an
// HLS packager would.
func encryptLikeCDN(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	padded := pkcs7Pad(t, plain)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func pkcs7Pad(t *testing.T, b []byte) []byte {
	t.Helper()
	pad := aes.BlockSize - len(b)%aes.BlockSize
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func TestDecryptSegmentRoundTripExplicitIV(t *testing.T) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	plain := []byte("TS packet payload \x47\x40\x00\x10 repeated to cross one or more blocks 1234567890")
	sealed := encryptLikeCDN(t, key, iv, plain)
	seg := Segment{Key: &Key{Method: "AES-128", IV: "00" /* ignored: explicit IV hex below */}}
	seg.Key.IV = hexEncode(iv)
	got, err := decryptSegment(seg, 1, key, sealed)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: got %d bytes", len(got))
	}
}

func TestDecryptSegmentDerivesIVFromSequence(t *testing.T) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	// No IV attribute: the IV is the 128-bit big-endian media sequence number.
	var iv [16]byte
	iv[15] = 42 // media sequence 42
	plain := bytes.Repeat([]byte{0x47, 0x01, 0x02, 0x03}, 64)
	sealed := encryptLikeCDN(t, key, iv[:], plain)
	seg := Segment{Key: &Key{Method: "AES-128"}} // no IV
	got, err := decryptSegment(seg, 42, key, sealed)
	if err != nil {
		t.Fatalf("decrypt with sequence IV: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("sequence-derived IV round trip mismatch")
	}
}

func TestDecryptSegmentRejectsBadInput(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 16)
	seg := Segment{Key: &Key{Method: "AES-128"}}
	if _, err := decryptSegment(seg, 0, key, nil); err == nil {
		t.Fatal("empty ciphertext must fail")
	}
	if _, err := decryptSegment(seg, 0, key, []byte("short")); err == nil {
		t.Fatal("non-block-aligned ciphertext must fail")
	}
	// Validly sized but garbage ciphertext must fail PKCS7 validation, not
	// return corrupt bytes.
	if _, err := decryptSegment(seg, 0, key, bytes.Repeat([]byte{0x00}, 32)); err == nil {
		t.Fatal("garbage ciphertext must fail")
	}
	// Bad explicit IV length.
	seg.Key.IV = "00"
	if _, err := decryptSegment(seg, 0, key, bytes.Repeat([]byte{0x10}, 32)); err == nil {
		t.Fatal("short IV must fail")
	}
}

func TestParseHexKey(t *testing.T) {
	if k, err := parseHexKey("000102030405060708090a0b0c0d0e0f"); err != nil || len(k) != 16 {
		t.Fatalf("hex key: %v", err)
	}
	if k, err := parseHexKey(" 000102030405060708090a0b0c0d0e0f\r\n"); err != nil || len(k) != 16 {
		t.Fatalf("whitespace-wrapped hex key: %v", err)
	}
	if _, err := parseHexKey("zz"); err == nil {
		t.Fatal("garbage key must fail")
	}
	if _, err := parseHexKey("00"); err == nil {
		t.Fatal("short key must fail")
	}
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexDigits[c>>4], hexDigits[c&0x0f])
	}
	return string(out)
}
