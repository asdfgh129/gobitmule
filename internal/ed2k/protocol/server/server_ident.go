package server

// OP_SERVERIDENT (0x41) is the packet a server sends right after a successful
// login. Besides the server hash and endpoint it carries the tag list that
// holds ST_UDPFLAGS, ST_UDPKEY and ST_UDPKEYIP; without those the client can
// neither choose between OP_GLOBGETSOURCES and OP_GLOBGETSOURCES2 nor enable
// the server UDP RC4 envelope (ServerSocket.cpp, CServer::ProcessTag).

import (
	"bytes"
	"strconv"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// Server tag ids (opcodes.h ST_*).
const (
	TagServerName    byte = 0x01
	TagDescription   byte = 0x0B
	TagVersion       byte = 0x91
	TagUDPFlags      byte = 0x92
	TagLowIDUsers    byte = 0x94
	TagUDPKey        byte = 0x95
	TagUDPKeyIP      byte = 0x96
	TagTCPObfuscPort byte = 0x97
	TagUDPObfuscPort byte = 0x98
)

// Server UDP capability flags (Server.h SRV_UDPFLG_*).
const (
	UDPFlagExtGetSources  uint32 = 0x00000001
	UDPFlagExtGetFiles    uint32 = 0x00000002
	UDPFlagNewTags        uint32 = 0x00000008
	UDPFlagUnicode        uint32 = 0x00000010
	UDPFlagExtGetSources2 uint32 = 0x00000020
	UDPFlagLargeFiles     uint32 = 0x00000100
	UDPFlagUDPObfuscation uint32 = 0x00000200
	UDPFlagTCPObfuscation uint32 = 0x00000400
)

type ServerIdent struct {
	Hash protocol.Hash
	IP   uint32
	Port uint16
	Tags protocol.TagList

	// Convenience fields decoded from Tags for the session.
	UDPFlags    uint32
	UDPKey      uint32
	UDPKeyIP    uint32
	Version     string
	LowIDUsers  uint32
	Name        string
	Description string
}

func (s *ServerIdent) Get(src *bytes.Reader) error {
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	ip, err := protocol.ReadUInt32(src)
	if err != nil {
		return err
	}
	port, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	var tags protocol.TagList
	if err := tags.Get(src); err != nil {
		return err
	}
	s.Hash, s.IP, s.Port, s.Tags = hash, ip, port, tags
	for _, tag := range tags {
		switch tag.ID {
		case TagServerName:
			s.Name = tag.String
		case TagDescription:
			s.Description = tag.String
		case TagVersion:
			if tag.String != "" {
				s.Version = tag.String
			} else {
				s.Version = formatServerVersion(tag.UInt32)
			}
		case TagUDPFlags:
			s.UDPFlags = tag.UInt32
		case TagLowIDUsers:
			s.LowIDUsers = tag.UInt32
		case TagUDPKey:
			s.UDPKey = tag.UInt32
		case TagUDPKeyIP:
			s.UDPKeyIP = tag.UInt32
		}
	}
	return nil
}

func (s ServerIdent) Put(dst *bytes.Buffer) error {
	if err := protocol.WriteHash(dst, s.Hash); err != nil {
		return err
	}
	if err := protocol.WriteUInt32(dst, s.IP); err != nil {
		return err
	}
	if err := protocol.WriteUInt16(dst, s.Port); err != nil {
		return err
	}
	return s.Tags.Put(dst)
}

func (s ServerIdent) BytesCount() int { return 16 + 4 + 2 + s.Tags.BytesCount() }

// SupportsUDPGetSources2 reports whether the server accepts the 20 byte
// OP_GLOBGETSOURCES2 records (hash + file size) instead of the legacy 16 byte
// OP_GLOBGETSOURCES form.
func (s ServerIdent) SupportsUDPGetSources2() bool {
	return s.UDPFlags&UDPFlagExtGetSources2 != 0
}

// SupportsUDPObfuscation reports whether the server-key RC4 envelope is
// available for datagrams.
func (s ServerIdent) SupportsUDPObfuscation() bool {
	return s.UDPFlags&UDPFlagUDPObfuscation != 0
}

func formatServerVersion(value uint32) string {
	return strconv.FormatUint(uint64(value>>16), 10) + "." + strconv.FormatUint(uint64(value&0xffff), 10)
}
