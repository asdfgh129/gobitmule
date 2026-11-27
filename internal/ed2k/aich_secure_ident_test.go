package goed2k

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// #4: AICH request/answer wire format round-trips including recovery data.
func TestAICHPacketRoundTrip(t *testing.T) {
	root := sha1.Sum([]byte("master"))
	req := clientproto.AICHRequest{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		Part:       7,
		MasterHash: root,
	}
	var buf bytes.Buffer
	if err := req.Put(&buf); err != nil {
		t.Fatalf("put request: %v", err)
	}
	if buf.Len() != req.BytesCount() {
		t.Fatalf("BytesCount=%d wrote=%d", req.BytesCount(), buf.Len())
	}
	var got clientproto.AICHRequest
	if err := got.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get request: %v", err)
	}
	if got.Part != 7 || got.MasterHash != root || got.Hash != req.Hash {
		t.Fatalf("AICH request round-trip failed: %+v", got)
	}

	ans := clientproto.AICHAnswer{Hash: req.Hash, Part: 7, MasterHash: root,
		RecoveryData: bytes.Repeat([]byte{0x2A}, 40)}
	buf.Reset()
	if err := ans.Put(&buf); err != nil {
		t.Fatalf("put answer: %v", err)
	}
	var gotAns clientproto.AICHAnswer
	if err := gotAns.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get answer: %v", err)
	}
	if gotAns.Part != 7 || !bytes.Equal(gotAns.RecoveryData, ans.RecoveryData) {
		t.Fatalf("AICH answer round-trip failed: %+v", gotAns)
	}
}

// #4: a recovery answer whose master hash does not match the transfer's AICH
// root must be rejected (no poisoning of the recovery store).
func TestAICHAnswerRejectsWrongRoot(t *testing.T) {
	session := NewSession(NewSettings())
	root := sha1.Sum([]byte("correct"))
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE"),
		CreateTime: CurrentTime(), Size: 2 * 1024 * 1024,
		AICHRoot: root[:],
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	pc := NewPeerConnection(session, protocol.Endpoint{}, transfer, nil)
	wrongRoot := sha1.Sum([]byte("wrong"))
	answer := clientproto.AICHAnswer{
		Hash: transfer.GetHash(), Part: 0, MasterHash: wrongRoot,
		RecoveryData: bytes.Repeat([]byte{0x01}, 20),
	}
	pc.HandleAICHAnswer(&answer)
	if _, ok := transfer.AICHPartRecovery(0); ok {
		t.Fatal("wrong-root AICH answer was accepted")
	}
	answer.MasterHash = root
	pc.HandleAICHAnswer(&answer)
	if data, ok := transfer.AICHPartRecovery(0); !ok || len(data) != 20 {
		t.Fatal("valid AICH answer was not stored")
	}
}

// #5: a valid RSA signature over our challenge verifies and marks the peer's
// user hash as authenticated; an invalid signature does not.
func TestSecureIdentChallengeResponse(t *testing.T) {
	session := NewSession(NewSettings())
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	userHash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	pc := NewPeerConnection(session, protocol.Endpoint{}, nil, nil)
	pc.remoteHash = userHash

	// Peer sends public key -> we issue a challenge.
	pc.HandlePublicKey(&clientproto.PublicKey{Key: der})
	if !pc.secIdent.awaitingSig || pc.secIdent.challenge == 0 {
		t.Fatal("challenge was not issued")
	}
	challenge := make([]byte, 4)
	for i := range challenge {
		challenge[i] = byte(pc.secIdent.challenge >> (8 * i))
	}
	digest := sha1.Sum(challenge)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	pc.HandleSignature(&clientproto.Signature{PublicKey: der, Signature: sig})
	if !pc.secIdent.verified || !session.Credits().IsVerified(userHash) {
		t.Fatal("valid Secure Ident signature was not accepted")
	}

	// A forged signature must not verify.
	pc2 := NewPeerConnection(session, protocol.Endpoint{}, nil, nil)
	pc2.remoteHash = protocol.MustHashFromString("C7A91B4E8D2F3A0B5E6D7C8091A2B3C4")
	pc2.HandlePublicKey(&clientproto.PublicKey{Key: der})
	pc2.HandleSignature(&clientproto.Signature{PublicKey: der, Signature: bytes.Repeat([]byte{0}, len(sig))})
	if pc2.secIdent.verified {
		t.Fatal("forged signature was accepted")
	}
}
