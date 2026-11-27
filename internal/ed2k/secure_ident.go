package goed2k

// #5 Secure Ident state living on a peer connection. eMule binds a user hash
// to an RSA key pair; a challenge is signed by the key owner so credit cannot
// be stolen by cloning a user hash.
//
// Protocol flow (BaseClient::SendSecIdentStatePacket / ProcessPublicKeyPacket /
// ProcessSignaturePacket):
//
//   - both sides, once the eMule info packets are exchanged, send
//     OP_SECIDENTSTATE with a random challenge: state 2 when they do not have
//     the peer's public key yet, state 1 when they only need a signature;
//   - the receiving side answers with OP_PUBLICKEY (when asked) and
//     OP_SIGNATURE;
//   - the signature covers <verifier public key><verifier challenge> and is
//     produced with the signer's private key, so the verifier checks it with
//     the peer's public key against its own key and challenge.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/binary"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// secureIdentIdentity is the session-wide long term RSA key pair advertised by
// OP_PUBLICKEY. eMule keeps one per installation; keeping it on the session
// gives the same stability for the lifetime of the daemon.
type secureIdentIdentity struct {
	mu     sync.Mutex
	key    *rsa.PrivateKey
	derKey []byte
}

// ensure returns the key pair and its DER encoding, generating it on first use.
// A 1024 bit key matches eMule: the DER form must fit the 250 byte cap that
// OP_PUBLICKEY allows.
func (i *secureIdentIdentity) ensure() (*rsa.PrivateKey, []byte) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.key != nil && len(i.derKey) > 0 {
		return i.key, i.derKey
	}
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, nil
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil || len(der) > 250 {
		return nil, nil
	}
	i.key, i.derKey = key, der
	return key, der
}

type secureIdentState struct {
	pubKey          []byte // peer's DER X.509 RSA public key
	challenge       uint32 // random value we asked the peer to sign
	remoteChallenge uint32 // random value the peer asked us to sign
	awaitingSig     bool
	verified        bool
	stateSent       bool
}

// StartSecureIdent begins the handshake after both Hello packets were parsed.
// eMule triggers this from CUpDownClient::InfoPacketsReceived when the remote
// MiscOptions advertise Secure Ident support.
func (p *PeerConnection) StartSecureIdent() {
	if p == nil || p.session == nil || p.secIdent.stateSent {
		return
	}
	if p.remotePeerInfo.Misc1.SupportSecIdent <= 0 {
		return
	}
	challenge := randomSecureIdentChallenge()
	if challenge == 0 {
		return
	}
	state := clientproto.SecIdentStateKeyAndSignNeeded
	if len(p.secIdent.pubKey) > 0 {
		state = clientproto.SecIdentStateSignatureNeeded
	}
	p.secIdent.stateSent = true
	p.secIdent.challenge = challenge
	p.sendSecIdentState(state, challenge)
}

// HandleSecIdentState records the peer's requested state and answers it: when
// the peer lacks our key we publish OP_PUBLICKEY, then send the signature for
// its challenge.
func (p *PeerConnection) HandleSecIdentState(value *clientproto.SecIdentState) {
	if value == nil || p.session == nil {
		return
	}
	debugPeerf("peer %s Secure Ident state=%d challenge=%d", p.endpoint.String(), value.State, value.Challenge)
	switch value.State {
	case clientproto.SecIdentStateUnavailable:
		return
	case clientproto.SecIdentStateSignatureNeeded, clientproto.SecIdentStateKeyAndSignNeeded:
		p.secIdent.remoteChallenge = value.Challenge
	default:
		return
	}
	if value.Challenge == 0 {
		return
	}
	if value.State == clientproto.SecIdentStateKeyAndSignNeeded {
		p.sendPublicKey()
	}
	// The signature covers the peer's key, so we can only sign once we have it.
	if len(p.secIdent.pubKey) > 0 {
		p.sendSignature()
	}
}

// HandlePublicKey stores a peer's RSA public key and, when the peer already
// asked us to sign its challenge, answers with the signature.
func (p *PeerConnection) HandlePublicKey(value *clientproto.PublicKey) {
	if value == nil || len(value.Key) == 0 {
		return
	}
	if _, err := x509.ParsePKIXPublicKey(value.Key); err != nil {
		debugPeerf("peer %s Secure Ident public key rejected: %v", p.endpoint.String(), err)
		return
	}
	p.secIdent.pubKey = append([]byte(nil), value.Key...)
	debugPeerf("peer %s Secure Ident public key stored", p.endpoint.String())
	if p.secIdent.remoteChallenge != 0 {
		p.sendSignature()
	}
}

// HandleSignature verifies the peer's signature over our own public key and
// challenge, then marks the connection's user hash as authenticated.
func (p *PeerConnection) HandleSignature(value *clientproto.Signature) {
	if value == nil || p.session == nil || len(p.secIdent.pubKey) == 0 {
		return
	}
	if p.secIdent.challenge == 0 {
		debugPeerf("peer %s Secure Ident signature without challenge", p.endpoint.String())
		return
	}
	_, myKeyDER := p.session.secureIdent.ensure()
	if len(myKeyDER) == 0 {
		return
	}
	pub, err := x509.ParsePKIXPublicKey(p.secIdent.pubKey)
	if err != nil {
		return
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return
	}
	// eMule signs <verifier public key><verifier challenge>; the verifier is us.
	if err := verifyIdentPayload(rsaPub, myKeyDER, p.secIdent.challenge, value.Signature); err != nil {
		debugPeerf("peer %s Secure Ident signature invalid: %v", p.endpoint.String(), err)
		return
	}
	p.secIdent.verified = true
	p.secIdent.awaitingSig = false
	if !p.remoteHash.Equal(protocol.Invalid) {
		p.session.Credits().MarkVerified(p.remoteHash)
	}
	debugPeerf("peer %s Secure Ident verified", p.endpoint.String())
}

// SecureIdentVerified reports whether the peer proved possession of its key.
func (p *PeerConnection) SecureIdentVerified() bool {
	return p != nil && p.secIdent.verified
}

func (p *PeerConnection) sendSecIdentState(state byte, challenge uint32) {
	packet := clientproto.SecIdentState{State: state, Challenge: challenge}
	raw, err := p.combiner.Pack("client.SecIdentState", &packet)
	if err != nil {
		return
	}
	p.QueuePacket(raw)
	debugPeerf("peer %s -> Secure Ident state=%d", p.endpoint.String(), state)
}

func (p *PeerConnection) sendPublicKey() {
	_, der := p.session.secureIdent.ensure()
	if len(der) == 0 {
		return
	}
	packet := clientproto.PublicKey{Key: der}
	raw, err := p.combiner.Pack("client.PublicKey", &packet)
	if err != nil {
		return
	}
	p.QueuePacket(raw)
	debugPeerf("peer %s -> Secure Ident public key %d bytes", p.endpoint.String(), len(der))
}

// sendSignature signs the peer's public key with our own challenge value. The
// peer verifies it against its own key and the challenge it sent us.
func (p *PeerConnection) sendSignature() {
	if p.session == nil || p.secIdent.remoteChallenge == 0 || len(p.secIdent.pubKey) == 0 {
		return
	}
	key, _ := p.session.secureIdent.ensure()
	if key == nil {
		return
	}
	signature, err := signIdentPayload(key, p.secIdent.pubKey, p.secIdent.remoteChallenge)
	if err != nil {
		return
	}
	packet := clientproto.Signature{Signature: signature}
	raw, err := p.combiner.Pack("client.Signature", &packet)
	if err != nil {
		return
	}
	p.QueuePacket(raw)
	p.secIdent.awaitingSig = false
	debugPeerf("peer %s -> Secure Ident signature %d bytes", p.endpoint.String(), len(signature))
}

func randomSecureIdentChallenge() uint32 {
	var buf [4]byte
	for attempt := 0; attempt < 4; attempt++ {
		if _, err := rand.Read(buf[:]); err != nil {
			return 0
		}
		if value := binary.LittleEndian.Uint32(buf[:]); value != 0 {
			return value
		}
	}
	return 0
}

// signIdentPayload signs <verifier public key><verifier challenge> with our
// private key, exactly like CClientCreditsList::CreateSignature.
func signIdentPayload(key *rsa.PrivateKey, verifierKey []byte, challenge uint32) ([]byte, error) {
	payload := make([]byte, 0, len(verifierKey)+4)
	payload = append(payload, verifierKey...)
	payload = binary.LittleEndian.AppendUint32(payload, challenge)
	digest := sha1.Sum(payload)
	return rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, digest[:])
}

// verifyIdentPayload mirrors CClientCreditsList::VerifyIdent: the signed data
// is our own public key plus the challenge we sent.
func verifyIdentPayload(signerKey *rsa.PublicKey, verifierKey []byte, challenge uint32, signature []byte) error {
	payload := make([]byte, 0, len(verifierKey)+4)
	payload = append(payload, verifierKey...)
	payload = binary.LittleEndian.AppendUint32(payload, challenge)
	digest := sha1.Sum(payload)
	return rsa.VerifyPKCS1v15(signerKey, crypto.SHA1, digest[:], signature)
}
