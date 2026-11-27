package client

// #5: eMule Secure Ident (RSA challenge-response identity verification).
// OP_PUBLICKEY / OP_SIGNATURE / OP_SECIDENTSTATE carry an X.509-encoded RSA
// public key and a signature over a random challenge, binding a user hash to
// its real owner so credit cannot be stolen by spoofing an ID.
//
// Wire form (EMuleProt), matching eMule BaseClient.cpp:
//
//	OP_PUBLICKEY:     <keylen 1><DER public key bytes>
//	OP_SIGNATURE:     v1 <siglen 1><signature bytes>
//	                  v2 <siglen 1><signature bytes><sigIPused 1>
//	OP_SECIDENTSTATE: <state 1><challenge 4>
//
// The signature payload is NOT carried in the packet: both sides sign
// <verifier public key><verifier challenge> with the signer's private key
// (CClientCreditsList::CreateSignature / VerifyIdent).
//
// SECIDENTSTATE values are the protocol constants, not the local client state
// machine: 0 = unavailable, 1 = signature needed (peer already has our key),
// 2 = public key and signature needed.

import (
	"bytes"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

const (
	SecIdentStateUnavailable      byte = 0
	SecIdentStateSignatureNeeded  byte = 1
	SecIdentStateKeyAndSignNeeded byte = 2
	maxSecureIdentBlob                 = 250
)

type PublicKey struct {
	Key []byte // DER-encoded X.509 RSA public key
}

func (p *PublicKey) Get(src *bytes.Reader) error {
	n, err := src.ReadByte()
	if err != nil {
		return err
	}
	if int(n) > maxSecureIdentBlob || int(n) == 0 {
		return errors.New("invalid public key length")
	}
	p.Key = make([]byte, int(n))
	if _, err := src.Read(p.Key); err != nil {
		return err
	}
	return nil
}

func (p PublicKey) Put(dst *bytes.Buffer) error {
	if len(p.Key) == 0 || len(p.Key) > maxSecureIdentBlob {
		return errors.New("invalid public key length")
	}
	_ = dst.WriteByte(byte(len(p.Key)))
	_, err := dst.Write(p.Key)
	return err
}

func (p PublicKey) BytesCount() int { return 1 + len(p.Key) }

// Signature is the raw RSA signature. eMule never repeats the public key here:
// the key arrived earlier in OP_PUBLICKEY and both sides cached it per user.
type Signature struct {
	Signature []byte
	UsedIPKind byte // v2 only; 0 when the packet carries no trailing byte
}

func (s *Signature) Get(src *bytes.Reader) error {
	sigLen, err := src.ReadByte()
	if err != nil || int(sigLen) > maxSecureIdentBlob || sigLen == 0 {
		return errors.New("invalid signature length")
	}
	s.Signature = make([]byte, int(sigLen))
	if _, err := src.Read(s.Signature); err != nil {
		return err
	}
	// v2 appends the challenge-IP kind; v1 stops after the signature.
	if src.Len() > 0 {
		s.UsedIPKind, _ = src.ReadByte()
	}
	return nil
}

func (s Signature) Put(dst *bytes.Buffer) error {
	if len(s.Signature) == 0 || len(s.Signature) > maxSecureIdentBlob {
		return errors.New("invalid signature length")
	}
	_ = dst.WriteByte(byte(len(s.Signature)))
	if _, err := dst.Write(s.Signature); err != nil {
		return err
	}
	if s.UsedIPKind != 0 {
		_ = dst.WriteByte(s.UsedIPKind)
	}
	return nil
}

func (s Signature) BytesCount() int {
	n := 1 + len(s.Signature)
	if s.UsedIPKind != 0 {
		n++
	}
	return n
}

type SecIdentState struct {
	State     byte
	Challenge uint32
}

func (s *SecIdentState) Get(src *bytes.Reader) error {
	state, err := src.ReadByte()
	if err != nil {
		return err
	}
	challenge, err := protocol.ReadUInt32(src)
	if err != nil {
		return err
	}
	s.State, s.Challenge = state, challenge
	return nil
}

func (s SecIdentState) Put(dst *bytes.Buffer) error {
	_ = dst.WriteByte(s.State)
	return protocol.WriteUInt32(dst, s.Challenge)
}

func (s SecIdentState) BytesCount() int { return 5 }
