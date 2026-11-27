package goed2k

import (
	"bytes"
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

// #5: the Secure Ident signature covers <verifier public key><verifier
// challenge>, is verified with the signer's key, and a forged/foreign payload
// is rejected.
func TestSecureIdentChallengeResponse(t *testing.T) {
	session := NewSession(NewSettings())
	myKey, myDER := session.secureIdent.ensure()
	if myKey == nil || len(myDER) == 0 {
		t.Fatal("session identity was not generated")
	}
	peerKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	peerDER, err := x509.MarshalPKIXPublicKey(&peerKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	userHash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	pc := NewPeerConnection(session, protocol.Endpoint{}, nil, nil)
	pc.remoteHash = userHash
	pc.secIdent.pubKey = peerDER
	pc.secIdent.challenge = 0x11223344

	// The peer signs our key plus the challenge we sent (eMule VerifyIdent).
	sig, err := signIdentPayload(peerKey, myDER, pc.secIdent.challenge)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	pc.HandleSignature(&clientproto.Signature{Signature: sig})
	if !pc.secIdent.verified || !session.Credits().IsVerified(userHash) {
		t.Fatal("valid Secure Ident signature was not accepted")
	}

	// A signature over a different challenge must not verify.
	pc2 := NewPeerConnection(session, protocol.Endpoint{}, nil, nil)
	pc2.remoteHash = protocol.MustHashFromString("C7A91B4E8D2F3A0B5E6D7C8091A2B3C4")
	pc2.secIdent.pubKey = peerDER
	pc2.secIdent.challenge = 0x11223344
	other, err := signIdentPayload(peerKey, myDER, 0x55667788)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	pc2.HandleSignature(&clientproto.Signature{Signature: other})
	if pc2.secIdent.verified {
		t.Fatal("signature over a different challenge was accepted")
	}

	// Our own signature verifies against our key for the peer's challenge.
	ours, err := signIdentPayload(myKey, peerDER, 0xAABBCCDD)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := verifyIdentPayload(&myKey.PublicKey, peerDER, 0xAABBCCDD, ours); err != nil {
		t.Fatalf("our signature did not verify: %v", err)
	}
}

// #5: OP_SIGNATURE carries only the signature (plus the optional v2 IP kind);
// it never repeats the public key, unlike the pre-fix layout.
func TestSecureIdentSignatureWireFormat(t *testing.T) {
	var buf bytes.Buffer
	sig := clientproto.Signature{Signature: bytes.Repeat([]byte{0x11}, 64)}
	if err := sig.Put(&buf); err != nil {
		t.Fatalf("put: %v", err)
	}
	if buf.Len() != 65 {
		t.Fatalf("v1 signature wire length=%d, want 65", buf.Len())
	}
	var got clientproto.Signature
	if err := got.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got.Signature, sig.Signature) || got.UsedIPKind != 0 {
		t.Fatalf("v1 round-trip mismatch: %+v", got)
	}

	buf.Reset()
	v2 := clientproto.Signature{Signature: bytes.Repeat([]byte{0x22}, 64), UsedIPKind: 2}
	if err := v2.Put(&buf); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if buf.Len() != 66 {
		t.Fatalf("v2 signature wire length=%d, want 66", buf.Len())
	}
	var gotV2 clientproto.Signature
	if err := gotV2.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get v2: %v", err)
	}
	if gotV2.UsedIPKind != 2 || !bytes.Equal(gotV2.Signature, v2.Signature) {
		t.Fatalf("v2 round-trip mismatch: %+v", gotV2)
	}
}
