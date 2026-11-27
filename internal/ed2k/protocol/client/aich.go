package client

// #4: AICH (Advanced Intelligent Corruption Handling) recovery packets.
// eMule's OP_AICHREQUEST / OP_AICHANSWER (0x9B / 0x9C) let a downloader that
// detected a corrupt piece ask a peer for the SHA-1 block hashes of that part,
// so it can pinpoint the damaged 180 KiB block instead of re-downloading the
// whole 9.28 MB part. Wire form (EMuleProt):
//
//	<hash 16><part uint16><aich master hash 20>
//	answer: <hash 16><part uint16><aich master hash 20><recovery data...>

import (
	"bytes"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

const (
	AichHashSize = 20
)

type AICHRequest struct {
	Hash       protocol.Hash
	Part       uint16
	MasterHash [AichHashSize]byte
}

func (a *AICHRequest) Get(src *bytes.Reader) error {
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	part, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	raw := make([]byte, AichHashSize)
	if n, err := src.Read(raw); err != nil || n != AichHashSize {
		return errors.New("short AICH master hash")
	}
	a.Hash, a.Part = hash, part
	copy(a.MasterHash[:], raw)
	return nil
}

func (a AICHRequest) Put(dst *bytes.Buffer) error {
	if err := protocol.WriteHash(dst, a.Hash); err != nil {
		return err
	}
	if err := protocol.WriteUInt16(dst, a.Part); err != nil {
		return err
	}
	_, err := dst.Write(a.MasterHash[:])
	return err
}

func (a AICHRequest) BytesCount() int { return 16 + 2 + AichHashSize }

type AICHAnswer struct {
	Hash        protocol.Hash
	Part        uint16
	MasterHash  [AichHashSize]byte
	RecoveryData []byte
}

func (a *AICHAnswer) Get(src *bytes.Reader) error {
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	part, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	raw := make([]byte, AichHashSize)
	if _, err := src.Read(raw); err != nil {
		return errors.New("short AICH master hash")
	}
	a.Hash, a.Part = hash, part
	copy(a.MasterHash[:], raw)
	a.RecoveryData = make([]byte, src.Len())
	_, err = src.Read(a.RecoveryData)
	return err
}

func (a AICHAnswer) Put(dst *bytes.Buffer) error {
	if err := protocol.WriteHash(dst, a.Hash); err != nil {
		return err
	}
	if err := protocol.WriteUInt16(dst, a.Part); err != nil {
		return err
	}
	if _, err := dst.Write(a.MasterHash[:]); err != nil {
		return err
	}
	_, err := dst.Write(a.RecoveryData)
	return err
}

// BytesCount implements protocol.Serializable (fixed header + recovery data).
func (a AICHAnswer) BytesCount() int { return 16 + 2 + AichHashSize + len(a.RecoveryData) }

// PayloadSize reports the recovery-data length carried after the fixed header.
func (a AICHAnswer) PayloadSize() int { return len(a.RecoveryData) }
