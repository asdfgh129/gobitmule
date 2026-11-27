package goed2k

// eMule-compatible 384 bit RSA identity.
//
// SecureIdent in eMule is a 384 bit RSA key (opcodes.h: RSAKEYSIZE 384) and the
// public key travels as an X.509 SubjectPublicKeyInfo that must fit
// CClientCredits::SetSecureIdent's MAXPUBKEYSIZE (80 bytes). Go's crypto/rsa
// refuses to generate or use keys below 1024 bits ("crypto/rsa: 384-bit keys
// are insecure"), and a 1024 bit SPKI is 162 bytes, which eMule rejects before
// it even looks at the signature - the peer simply never answers with
// OP_SIGNATURE. This file therefore implements the small key with math/big:
// generation, PKCS#1 v1.5 (SHA-1) signing/verification and SPKI encoding.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
)

const (
	identKeyBits = 384
	// identPublicKeyLimit mirrors eMule's MAXPUBKEYSIZE.
	identPublicKeyLimit = 80
)

var (
	oidPublicKeyRSA = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	// sha1DigestInfo is the DER prefix of DigestInfo(SHA-1) used by PKCS#1 v1.5.
	sha1DigestInfo = []byte{0x30, 0x21, 0x30, 0x09, 0x06, 0x05, 0x2b, 0x0e, 0x03, 0x02, 0x1a, 0x05, 0x00, 0x04, 0x14}
)

// rsaIdentKey is a small RSA key pair. Only N, E and D are kept: P and Q are
// dropped after generation because eMule never needs CRT and keeping them
// would only enlarge the state file.
type rsaIdentKey struct {
	N *big.Int
	E int
	D *big.Int
}

// generateIdentKey creates a fresh 384 bit key with e = 65537.
func generateIdentKey() (*rsaIdentKey, error) {
	e := big.NewInt(65537)
	one := big.NewInt(1)
	for attempt := 0; attempt < 32; attempt++ {
		p, err := rand.Prime(rand.Reader, identKeyBits/2)
		if err != nil {
			return nil, err
		}
		q, err := rand.Prime(rand.Reader, identKeyBits/2)
		if err != nil {
			return nil, err
		}
		if p.Cmp(q) == 0 {
			continue
		}
		n := new(big.Int).Mul(p, q)
		if n.BitLen() != identKeyBits {
			continue
		}
		pm1 := new(big.Int).Sub(p, one)
		qm1 := new(big.Int).Sub(q, one)
		phi := new(big.Int).Mul(pm1, qm1)
		d := new(big.Int).ModInverse(e, phi)
		if d == nil {
			continue
		}
		return &rsaIdentKey{N: n, E: int(e.Int64()), D: d}, nil
	}
	return nil, errors.New("rsa ident: could not generate a 384 bit key")
}

// publicDER returns the X.509 SubjectPublicKeyInfo encoding eMule expects.
func (k *rsaIdentKey) publicDER() ([]byte, error) {
	if k == nil || k.N == nil {
		return nil, errors.New("rsa ident: nil key")
	}
	inner, err := asn1.Marshal(struct {
		N *big.Int
		E int
	}{k.N, k.E})
	if err != nil {
		return nil, err
	}
	der, err := asn1.Marshal(struct {
		Algo      pkix.AlgorithmIdentifier
		BitString asn1.BitString
	}{
		Algo:      pkix.AlgorithmIdentifier{Algorithm: oidPublicKeyRSA, Parameters: asn1.RawValue{Tag: 5}},
		BitString: asn1.BitString{Bytes: inner, BitLength: 8 * len(inner)},
	})
	if err != nil {
		return nil, err
	}
	if len(der) > identPublicKeyLimit {
		return nil, fmt.Errorf("rsa ident: public key is %d bytes, eMule accepts at most %d", len(der), identPublicKeyLimit)
	}
	return der, nil
}

// signSHA1 produces a PKCS#1 v1.5 signature over the SHA-1 digest.
func (k *rsaIdentKey) signSHA1(digest []byte) ([]byte, error) {
	if k == nil || k.D == nil {
		return nil, errors.New("rsa ident: no private key")
	}
	if len(digest) != sha1.Size {
		return nil, errors.New("rsa ident: digest is not SHA-1")
	}
	em, err := pkcs1v15EM(k.N, digest)
	if err != nil {
		return nil, err
	}
	m := new(big.Int).SetBytes(em)
	s := new(big.Int).Exp(m, k.D, k.N)
	size := (k.N.BitLen() + 7) / 8
	out := make([]byte, size)
	s.FillBytes(out)
	return out, nil
}

// pkcs1v15EM builds EM = 0x00 0x01 0xFF.. 0x00 || DigestInfo || digest.
func pkcs1v15EM(n *big.Int, digest []byte) ([]byte, error) {
	size := (n.BitLen() + 7) / 8
	tLen := len(sha1DigestInfo) + len(digest)
	if size < tLen+11 {
		return nil, errors.New("rsa ident: modulus too small for PKCS#1 v1.5")
	}
	em := make([]byte, size)
	em[1] = 0x01
	for i := 2; i < size-tLen-1; i++ {
		em[i] = 0xff
	}
	em[size-tLen-1] = 0x00
	copy(em[size-tLen:], sha1DigestInfo)
	copy(em[size-len(digest):], digest)
	return em, nil
}

// verifySHA1 checks a PKCS#1 v1.5 SHA-1 signature with a peer's public key.
func verifyIdentSignature(pubDER, digest, signature []byte) error {
	n, e, err := parseIdentPublicKey(pubDER)
	if err != nil {
		return err
	}
	if len(digest) != sha1.Size {
		return errors.New("rsa ident: digest is not SHA-1")
	}
	size := (n.BitLen() + 7) / 8
	if len(signature) != size {
		return fmt.Errorf("rsa ident: signature is %d bytes, key needs %d", len(signature), size)
	}
	s := new(big.Int).SetBytes(signature)
	m := new(big.Int).Exp(s, big.NewInt(int64(e)), n)
	em := make([]byte, size)
	m.FillBytes(em)
	want, err := pkcs1v15EM(n, digest)
	if err != nil {
		return err
	}
	if !bytes.Equal(em, want) {
		return errors.New("rsa ident: signature does not match")
	}
	return nil
}

// parseIdentPublicKey accepts both the X.509 SubjectPublicKeyInfo that eMule
// sends and the bare PKCS#1 RSAPublicKey some mods use.
func parseIdentPublicKey(der []byte) (*big.Int, int, error) {
	var spki struct {
		Algo      pkix.AlgorithmIdentifier
		BitString asn1.BitString
	}
	if rest, err := asn1.Unmarshal(der, &spki); err == nil && len(rest) == 0 && len(spki.BitString.Bytes) > 0 {
		var key struct {
			N *big.Int
			E int
		}
		if rest, err := asn1.Unmarshal(spki.BitString.Bytes, &key); err == nil && len(rest) == 0 && key.N != nil && key.E > 0 {
			return key.N, key.E, nil
		}
	}
	var key struct {
		N *big.Int
		E int
	}
	if rest, err := asn1.Unmarshal(der, &key); err == nil && len(rest) == 0 && key.N != nil && key.E > 0 {
		return key.N, key.E, nil
	}
	return nil, 0, errors.New("rsa ident: unrecognised public key encoding")
}

// marshalIdentKey serialises the private key for the client state file.
func (k *rsaIdentKey) marshalIdentKey() (n, d []byte, e int) {
	if k == nil || k.N == nil || k.D == nil {
		return nil, nil, 0
	}
	return k.N.Bytes(), k.D.Bytes(), k.E
}

// loadIdentKey rebuilds a key from the state file.
func loadIdentKey(nBytes, dBytes []byte, e int) *rsaIdentKey {
	if len(nBytes) == 0 || len(dBytes) == 0 || e <= 0 {
		return nil
	}
	return &rsaIdentKey{N: new(big.Int).SetBytes(nBytes), E: e, D: new(big.Int).SetBytes(dBytes)}
}
