package goed2k

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/binary"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// #5 Secure Ident state living on a peer connection. eMule binds a user hash
// to an RSA key pair; a challenge is signed by the key owner so credit cannot
// be stolen by cloning a user hash.
type secureIdentState struct {
	pubKey      []byte // peer's DER X.509 RSA public key
	challenge   uint32 // random value we asked the peer to sign
	awaitingSig bool
	verified    bool
	remoteState byte // last state the peer asked us to enter
}

// HandlePublicKey stores a peer's RSA public key and replies with a random
// challenge in OP_SECIDENTSTATE (state=SIGNATURENEEDED), mirroring eMule's
// CUpDownClient::ProcessPublicKeyPacket flow.
func (p *PeerConnection) HandlePublicKey(value *clientproto.PublicKey) {
	if value == nil || len(value.Key) == 0 {
		return
	}
	if _, err := x509.ParsePKIXPublicKey(value.Key); err != nil {
		debugPeerf("peer %s Secure Ident public key rejected: %v", p.endpoint.String(), err)
		return
	}
	var challenge [4]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return
	}
	p.secIdent.pubKey = append([]byte(nil), value.Key...)
	p.secIdent.challenge = binary.LittleEndian.Uint32(challenge[:])
	p.secIdent.awaitingSig = true
	p.secIdent.verified = false
	packet := clientproto.SecIdentState{
		State:     clientproto.SecIdentStateSignatureNeeded,
		Challenge: p.secIdent.challenge,
	}
	if raw, err := p.combiner.Pack("client.SecIdentState", &packet); err == nil {
		p.QueuePacket(raw)
	}
	debugPeerf("peer %s Secure Ident challenge sent", p.endpoint.String())
}

// HandleSignature verifies the peer's signature over the challenge we sent.
// A valid signature marks this connection's user hash as authenticated.
func (p *PeerConnection) HandleSignature(value *clientproto.Signature) {
	if value == nil || !p.secIdent.awaitingSig {
		return
	}
	p.secIdent.awaitingSig = false
	pub, err := x509.ParsePKIXPublicKey(value.PublicKey)
	if err != nil {
		return
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return
	}
	challenge := make([]byte, 4)
	binary.LittleEndian.PutUint32(challenge, p.secIdent.challenge)
	digest := sha1.Sum(challenge)
	if err := rsa.VerifyPKCS1v15(rsaPub, crypto.SHA1, digest[:], value.Signature); err != nil {
		debugPeerf("peer %s Secure Ident signature invalid: %v", p.endpoint.String(), err)
		return
	}
	// The signing key must be the same one we challenged.
	if !equalBytes(pubKeyBytes(value.PublicKey), p.secIdent.pubKey) {
		return
	}
	p.secIdent.verified = true
	if !p.remoteHash.Equal(protocol.Invalid) {
		p.session.Credits().MarkVerified(p.remoteHash)
	}
	debugPeerf("peer %s Secure Ident verified", p.endpoint.String())
}

// HandleSecIdentState records a peer's requested state. When a peer asks us
// for a signature we would sign our own persistent key; the challenge state
// is tracked so we never send an unsolicited signature.
func (p *PeerConnection) HandleSecIdentState(value *clientproto.SecIdentState) {
	if value == nil {
		return
	}
	p.secIdent.remoteState = value.State
	debugPeerf("peer %s Secure Ident state=%d challenge=%d", p.endpoint.String(), value.State, value.Challenge)
}

func pubKeyBytes(key []byte) []byte { return key }

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
