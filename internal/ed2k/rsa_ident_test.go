package goed2k

import (
	"crypto/sha1"
	"encoding/base64"
	"testing"
)

// eMule refuses any public key above MAXPUBKEYSIZE (80 bytes) before it even
// looks at the signature, which is why the identity must be a 384 bit key.
// This test pins that contract.
func TestIdentPublicKeyFitsEMuleLimit(t *testing.T) {
	key, err := generateIdentKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if key.N.BitLen() != identKeyBits {
		t.Fatalf("modulus is %d bits, want %d", key.N.BitLen(), identKeyBits)
	}
	der, err := key.publicDER()
	if err != nil {
		t.Fatalf("publicDER: %v", err)
	}
	if len(der) > identPublicKeyLimit {
		t.Fatalf("public key is %d bytes, eMule accepts at most %d", len(der), identPublicKeyLimit)
	}
	if n, e, err := parseIdentPublicKey(der); err != nil || n.Cmp(key.N) != 0 || e != key.E {
		t.Fatalf("round-trip parse failed: err=%v", err)
	}
}

func TestIdentSignVerifyRoundTrip(t *testing.T) {
	signer, err := generateIdentKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	verifier, err := generateIdentKey()
	if err != nil {
		t.Fatalf("generate verifier: %v", err)
	}
	verifierDER, err := verifier.publicDER()
	if err != nil {
		t.Fatalf("verifier DER: %v", err)
	}
	challenge := uint32(0xDEADBEEF)
	sig, err := signIdentPayload(signer, verifierDER, challenge)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sig) != 48 {
		t.Fatalf("signature is %d bytes, want 48 (384 bit key)", len(sig))
	}
	signerDER, err := signer.publicDER()
	if err != nil {
		t.Fatalf("signer DER: %v", err)
	}
	if err := verifyIdentPayload(signerDER, verifierDER, challenge, sig); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// A different challenge must not verify.
	if err := verifyIdentPayload(signerDER, verifierDER, challenge+1, sig); err == nil {
		t.Fatal("signature over another challenge was accepted")
	}
	// A tampered signature must not verify.
	bad := append([]byte(nil), sig...)
	bad[0] ^= 0xff
	if err := verifyIdentPayload(signerDER, verifierDER, challenge, bad); err == nil {
		t.Fatal("tampered signature was accepted")
	}
}

// The state file must round-trip the private key so a restart keeps the same
// identity; eMule's cryptkey.dat exists for the same reason.
func TestIdentKeyStateRoundTrip(t *testing.T) {
	key, err := generateIdentKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	n, d, e := key.marshalIdentKey()
	restored := loadIdentKey(n, d, e)
	if restored == nil {
		t.Fatal("key was not restored")
	}
	if restored.N.Cmp(key.N) != 0 || restored.D.Cmp(key.D) != 0 || restored.E != key.E {
		t.Fatal("restored key differs")
	}
	digest := sha1.Sum([]byte("payload"))
	sig, err := restored.signSHA1(digest[:])
	if err != nil {
		t.Fatalf("sign with restored key: %v", err)
	}
	der, _ := key.publicDER()
	if err := verifyIdentSignature(der, digest[:], sig); err != nil {
		t.Fatalf("restored key signature did not verify: %v", err)
	}
	// base64 encoding of the state fields must survive the JSON layer.
	if _, err := base64.StdEncoding.DecodeString(base64.StdEncoding.EncodeToString(n)); err != nil {
		t.Fatalf("base64 round-trip: %v", err)
	}
}
