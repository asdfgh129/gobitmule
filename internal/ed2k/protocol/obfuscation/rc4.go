// Package obfuscation implements the eMule/eD2k "basic obfuscation"
// (CryptLayer) handshake at the protocol layer. It is a fork-local addition
// for ED-05 and mirrors the algorithm documented in eMule's
// EncryptedStreamSocket.cpp and aMule's matching implementation.
//
// Scope: this package provides deterministic, testable primitives — RC4,
// key derivation and the A/B handshake frame codec. Wiring it into the live
// TCP peer connection is a separate step; until that is done and verified
// against an obfuscation-only peer, ED-05 remains PARTIAL (not PASS).
package obfuscation

// Cipher is a small self-contained RC4 (KSA + PRGA). It is intentionally
// implemented locally rather than using crypto/rc4 so the discard behaviour
// required by eMule (first 1024 keystream bytes dropped) is explicit and
// testable, and so the fork carries no dependence on a deprecated stdlib API.
type Cipher struct {
	s [256]byte
	i byte
	j byte
}

// NewCipher initializes RC4 state from key via the key-scheduling algorithm.
func NewCipher(key []byte) *Cipher {
	c := &Cipher{}
	for k := range c.s {
		c.s[k] = byte(k)
	}
	j := 0
	for k := 0; k < 256; k++ {
		j = (j + int(c.s[k]) + int(key[k%len(key)])) & 0xff
		c.s[k], c.s[j] = c.s[j], c.s[k]
	}
	return c
}

// Discard drops the first n keystream bytes without producing output. eMule
// calls this with n=1024 for TCP client keys (RC4DiscardBytes).
func (c *Cipher) Discard(n int) {
	tmp := make([]byte, n)
	c.XORKeyStream(tmp, tmp)
}

// XORKeyStream applies the RC4 keystream to src, writing the result into dst.
// src and dst must have the same length; exact in-place overlap is allowed.
func (c *Cipher) XORKeyStream(dst, src []byte) {
	for k := range src {
		c.i += 1
		c.j += c.s[c.i]
		c.s[c.i], c.s[c.j] = c.s[c.j], c.s[c.i]
		dst[k] = src[k] ^ c.s[c.s[c.i]+c.s[c.j]]
	}
}
