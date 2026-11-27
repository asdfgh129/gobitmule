package client

import (
	"bytes"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// Source exchange 2 (SX2). eMule deprecated the original OP_REQUESTSOURCES /
// OP_ANSWERSOURCES (0x81/0x82) in 0.40; every client in the wild speaks 0x83 /
// 0x84, which is what eMule's ProcessRequestSources2 / ProcessAnswerSources
// handle.
//
// A peer only sends us SX2 traffic when our Hello advertises a non-zero
// SourceExchange1Ver (CT_EMULE_MISCOPTIONS1, bits 12-15). The answer's entry
// format is chosen by the *receiver's* advertised version, so a client that
// claims 1 only ever gets the 12-byte v1 entries.
const (
	// SourceExchangeVersion is the aMule/eMule SOURCEEXCHANGE2_VERSION. Version
	// 4 entries carry the user hash and crypt options; both are parsed below, so
	// requesting 4 gets the richest list peers can send.
	SourceExchangeVersion = 4
	// SourceExchangeVersion1 is kept as an alias for the older capability bit.
	SourceExchangeVersion1 = 1

	// sourceExchangeMaxEntries bounds an incoming answer. The count is attacker
	// controlled, and 500 * 12 bytes is already more sources than any part file
	// can use.
	sourceExchangeMaxEntries = 500

	sourceEntryV1Size = 4 + 2 + 4 + 2
	sourceEntryV2Size = sourceEntryV1Size + 16
	sourceEntryV4Size = sourceEntryV2Size + 1
)

var errSourceExchangeTooManyEntries = errors.New("source exchange: too many entries")

// RequestSources2 asks a peer for its source list for one file.
//
// Layout (little endian): uint8 version, uint16 reserved, 16 byte file hash.
type RequestSources2 struct {
	Version uint8
	Hash    protocol.Hash
}

func (r *RequestSources2) Get(src *bytes.Reader) error {
	version, err := src.ReadByte()
	if err != nil {
		return err
	}
	r.Version = version
	reserved, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	_ = reserved
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	r.Hash = hash
	return nil
}

func (r RequestSources2) Put(dst *bytes.Buffer) error {
	if err := dst.WriteByte(r.Version); err != nil {
		return err
	}
	if err := protocol.WriteUInt16(dst, 0); err != nil {
		return err
	}
	return protocol.WriteHash(dst, r.Hash)
}

func (r RequestSources2) BytesCount() int { return 1 + 2 + 16 }

// SourceExchangeSource is one source entry. Which fields are meaningful depends
// on the entry version that prefixes the enclosing AnswerSources2 packet.
//
// SX1 only ever carried high-ID sources: the entry is the source's address plus
// its 32-bit hybrid ID. Low-ID sources cannot be dialled at all, which is why
// eMule invented the richer SX2 entries instead of extending v1.
type SourceExchangeSource struct {
	// UserID is the "hybrid ID": the network-order IP for a high-ID source, or
	// its low ID when firewalled. This is the field a receiver dials; ServerIP
	// below is only the server it is connected to.
	UserID uint32
	// TCPPort is the source's listening port; zero means it cannot be dialled.
	TCPPort uint16
	// ServerIP / ServerPort identify the source's server. They are kept so the
	// struct round-trips byte for byte, but a low-ID source is not dialable.
	ServerIP   uint32
	ServerPort uint16
	// UserHash is only present from entry version 2 on.
	UserHash protocol.Hash
	// CryptOptions is only present in version 4.
	CryptOptions uint8
}

// AnswerSources2 is a peer's reply to RequestSources2: the sources it knows for
// one file.
//
// Layout: uint8 entry version, 16 byte file hash, uint16 count, then count
// entries whose width follows the version.
type AnswerSources2 struct {
	Version uint8
	Hash    protocol.Hash
	Sources []SourceExchangeSource
}

func (a *AnswerSources2) Get(src *bytes.Reader) error {
	version, err := src.ReadByte()
	if err != nil {
		return err
	}
	a.Version = version
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	a.Hash = hash
	count, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	if int(count) > sourceExchangeMaxEntries {
		return errSourceExchangeTooManyEntries
	}
	a.Sources = make([]SourceExchangeSource, 0, int(count))
	for i := 0; i < int(count); i++ {
		var entry SourceExchangeSource
		if entry.UserID, err = protocol.ReadUInt32(src); err != nil {
			return err
		}
		if entry.TCPPort, err = protocol.ReadUInt16(src); err != nil {
			return err
		}
		if entry.ServerIP, err = protocol.ReadUInt32(src); err != nil {
			return err
		}
		if entry.ServerPort, err = protocol.ReadUInt16(src); err != nil {
			return err
		}
		if version >= 2 {
			if entry.UserHash, err = protocol.ReadHash(src); err != nil {
				return err
			}
		}
		if version >= 4 {
			if entry.CryptOptions, err = src.ReadByte(); err != nil {
				return err
			}
		}
		a.Sources = append(a.Sources, entry)
	}
	return nil
}

func (a AnswerSources2) Put(dst *bytes.Buffer) error {
	if err := dst.WriteByte(a.Version); err != nil {
		return err
	}
	if err := protocol.WriteHash(dst, a.Hash); err != nil {
		return err
	}
	if err := protocol.WriteUInt16(dst, uint16(len(a.Sources))); err != nil {
		return err
	}
	for _, entry := range a.Sources {
		if err := protocol.WriteUInt32(dst, entry.UserID); err != nil {
			return err
		}
		if err := protocol.WriteUInt16(dst, entry.TCPPort); err != nil {
			return err
		}
		if err := protocol.WriteUInt32(dst, entry.ServerIP); err != nil {
			return err
		}
		if err := protocol.WriteUInt16(dst, entry.ServerPort); err != nil {
			return err
		}
		if a.Version >= 2 {
			if err := protocol.WriteHash(dst, entry.UserHash); err != nil {
				return err
			}
		}
		if a.Version >= 4 {
			if err := dst.WriteByte(entry.CryptOptions); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a AnswerSources2) BytesCount() int {
	size := 1 + 16 + 2
	switch {
	case a.Version >= 4:
		size += len(a.Sources) * sourceEntryV4Size
	case a.Version >= 2:
		size += len(a.Sources) * sourceEntryV2Size
	default:
		size += len(a.Sources) * sourceEntryV1Size
	}
	return size
}

// HybridUserID is the 32-bit "hybrid" ID eMule derives from a user hash: the
// last four bytes, network order. Peers use it to match an exchanged source
// against a client they already know, so sending zero is legal but sends a
// source with no identity attached.
func HybridUserID(hash protocol.Hash) uint32 {
	raw := hash.Bytes()
	if len(raw) < 16 {
		return 0
	}
	return uint32(raw[12])<<24 | uint32(raw[13])<<16 | uint32(raw[14])<<8 | uint32(raw[15])
}
