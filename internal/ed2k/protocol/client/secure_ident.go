package client

// #5: eMule Secure Ident (RSA challenge-response identity verification).
// OP_PUBLICKEY / OP_SIGNATURE / OP_SECIDENTSTATE carry an X.509-encoded RSA
// public key and a signature over a random challenge, binding a user hash to
// its real owner so credit cannot be stolen by spoofing an ID.
//
// Wire form (EMuleProt):
//
//	OP_PUBLICKEY:  <keylen 1><DER public key bytes>
//	OP_SIGNATURE:  <keylen 1><DER pubkey><siglen 1><signature bytes>[<ipkind 1>]
//	OP_SECIDENTSTATE: <state 1><challenge 4>

import (
	"bytes"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

const (
	SecIdentStateKeyAndSignNeeded = 1
	SecIdentStateSignatureNeeded  = 2
	SecIdentStateSigned           = 3
	maxSecureIdentBlob            = 250
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

type Signature struct {
	PublicKey  []byte
	Signature  []byte
	UsedIPKind byte // 0 when absent (eMule v1 signature form)
}

func (s *Signature) Get(src *bytes.Reader) error {
	keyLen, err := src.ReadByte()
	if err != nil || int(keyLen) > maxSecureIdentBlob || keyLen == 0 {
		return errors.New("invalid signature public key length")
	}
	s.PublicKey = make([]byte, int(keyLen))
	if _, err := src.Read(s.PublicKey); err != nil {
		return err
	}
	sigLen, err := src.ReadByte()
	if err != nil || int(sigLen) > maxSecureIdentBlob || sigLen == 0 {
		return errors.New("invalid signature length")
	}
	s.Signature = make([]byte, int(sigLen))
	if _, err := src.Read(s.Signature); err != nil {
		return err
	}
	if src.Len() > 0 {
		s.UsedIPKind, _ = src.ReadByte()
	}
	return nil
}

func (s Signature) Put(dst *bytes.Buffer) error {
	if len(s.PublicKey) == 0 || len(s.PublicKey) > maxSecureIdentBlob ||
		len(s.Signature) == 0 || len(s.Signature) > maxSecureIdentBlob {
		return errors.New("invalid signature blob length")
	}
	_ = dst.WriteByte(byte(len(s.PublicKey)))
	_, _ = dst.Write(s.PublicKey)
	_ = dst.WriteByte(byte(len(s.Signature)))
	_, _ = dst.Write(s.Signature)
	return nil
}

func (s Signature) BytesCount() int { return 2 + len(s.PublicKey) + len(s.Signature) }

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
