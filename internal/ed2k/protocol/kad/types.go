package kad

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

const (
	ProtocolHeader       byte = 0xE4
	PackedProtocolHeader byte = 0xE5
	SearchResOp          byte = 0x3B
	SearchSrcReqOp       byte = 0x34

	TagTypeHash16    byte = 0x01
	TagTypeString    byte = 0x02
	TagTypeUint32    byte = 0x03
	TagTypeFloat32   byte = 0x04
	TagTypeBool      byte = 0x05
	TagTypeBoolArray byte = 0x06
	TagTypeBlob      byte = 0x07
	TagTypeUint16    byte = 0x08
	TagTypeUint8     byte = 0x09
	TagTypeBsob      byte = 0x0A
	TagTypeUint64    byte = 0x0B
	TagTypeStr1      byte = 0x11
	TagTypeStrMax    byte = 0x26 // STR22, accepted by eMule receive path

	TagEncryption  byte = 0xF3
	TagBuddyHash   byte = 0xF8
	TagClientLowID byte = 0xF9
	TagServerPort  byte = 0xFA
	TagServerIP    byte = 0xFB
	TagSourceUPort byte = 0xFC
	TagSourcePort  byte = 0xFD
	TagSourceIP    byte = 0xFE
	TagSourceType  byte = 0xFF
)

type ID struct {
	protocol.Hash
}

func NewID(hash protocol.Hash) ID {
	return ID{Hash: hash}
}

func (i *ID) Get(src *bytes.Reader) error {
	raw, err := protocol.ReadBytes(src, 16)
	if err != nil {
		return err
	}
	buf := make([]byte, 16)
	for idx := 0; idx < len(raw); idx++ {
		buf[(idx/4)*4+3-(idx%4)] = raw[idx]
	}
	hash, err := protocol.HashFromBytes(buf)
	if err != nil {
		return err
	}
	i.Hash = hash
	return nil
}

func (i ID) Put(dst *bytes.Buffer) error {
	raw := i.Hash.Bytes()
	for idx := 0; idx < len(raw); idx++ {
		if err := dst.WriteByte(raw[(idx/4)*4+3-(idx%4)]); err != nil {
			return err
		}
	}
	return nil
}

type Endpoint struct {
	IP      uint32
	UDPPort uint16
	TCPPort uint16
}

func (e *Endpoint) Get(src *bytes.Reader) error {
	if err := binary.Read(src, binary.LittleEndian, &e.IP); err != nil {
		return err
	}
	if err := binary.Read(src, binary.LittleEndian, &e.UDPPort); err != nil {
		return err
	}
	return binary.Read(src, binary.LittleEndian, &e.TCPPort)
}

func (e Endpoint) Put(dst *bytes.Buffer) error {
	if err := binary.Write(dst, binary.LittleEndian, e.IP); err != nil {
		return err
	}
	if err := binary.Write(dst, binary.LittleEndian, e.UDPPort); err != nil {
		return err
	}
	return binary.Write(dst, binary.LittleEndian, e.TCPPort)
}

func (e Endpoint) ED2K() protocol.Endpoint {
	return protocol.NewEndpoint(int32(e.IP), int(e.TCPPort))
}

type Entry struct {
	ID       ID
	Endpoint Endpoint
	Version  byte
	Verified bool
}

func (e *Entry) Get(src *bytes.Reader) error {
	if err := e.ID.Get(src); err != nil {
		return err
	}
	if err := e.Endpoint.Get(src); err != nil {
		return err
	}
	version, err := src.ReadByte()
	if err != nil {
		return err
	}
	e.Version = version
	return nil
}

func (e Entry) Put(dst *bytes.Buffer) error {
	if err := e.ID.Put(dst); err != nil {
		return err
	}
	if err := e.Endpoint.Put(dst); err != nil {
		return err
	}
	return dst.WriteByte(e.Version)
}

type Tag struct {
	Type   byte
	ID     byte
	Name   string
	UInt64 uint64
	String string
	Bytes  []byte
}

func (t *Tag) Get(src *bytes.Reader) error {
	tagType, err := src.ReadByte()
	if err != nil {
		return err
	}
	t.Type = tagType & 0x7f
	t.ID = 0
	t.Name = ""
	t.String = ""
	t.UInt64 = 0
	t.Bytes = nil
	if tagType&0x80 != 0 {
		// Numeric tag ID. This is the classic eD2k/Kad form emitted by
		// our own packets and by most peers for well-known tags.
		tagID, err := src.ReadByte()
		if err != nil {
			return err
		}
		t.ID = tagID
	} else {
		// Named tag. eMule/aMule write the name with CFileDataIO::WriteString,
		// i.e. a little-endian uint16 length followed by the raw name bytes.
		// A one-character name is canonicalised to its numeric ID by aMule.
		nameSize, err := protocol.ReadUInt16(src)
		if err != nil {
			return err
		}
		rawName, err := protocol.ReadBytes(src, int(nameSize))
		if err != nil {
			return err
		}
		if nameSize == 1 {
			t.ID = rawName[0]
		} else {
			t.Name = string(rawName)
		}
	}
	switch t.Type {
	case TagTypeHash16:
		value, err := protocol.ReadBytes(src, 16)
		if err != nil {
			return err
		}
		t.Bytes = value
	case TagTypeBool:
		value, err := src.ReadByte()
		if err != nil {
			return err
		}
		t.UInt64 = uint64(value)
	case TagTypeFloat32:
		value, err := protocol.ReadUInt32(src)
		if err != nil {
			return err
		}
		t.UInt64 = uint64(value)
	case TagTypeBoolArray:
		bitCount, err := protocol.ReadUInt16(src)
		if err != nil {
			return err
		}
		t.UInt64 = uint64(bitCount)
		// eMule/aMule use len/8 + 1 bytes for a bool array on the wire.
		t.Bytes, err = protocol.ReadBytes(src, int(bitCount/8)+1)
		return err
	case TagTypeBlob:
		size, err := protocol.ReadUInt32(src)
		if err != nil {
			return err
		}
		if uint64(size) > uint64(src.Len()) {
			return errors.New("invalid kad blob tag size")
		}
		value, err := protocol.ReadBytes(src, int(size))
		if err != nil {
			return err
		}
		t.Bytes = value
	case TagTypeBsob:
		size, err := src.ReadByte()
		if err != nil {
			return err
		}
		value, err := protocol.ReadBytes(src, int(size))
		if err != nil {
			return err
		}
		t.Bytes = value
	case TagTypeUint8:
		value, err := src.ReadByte()
		if err != nil {
			return err
		}
		t.UInt64 = uint64(value)
	case TagTypeUint16:
		value, err := protocol.ReadUInt16(src)
		if err != nil {
			return err
		}
		t.UInt64 = uint64(value)
	case TagTypeUint32:
		value, err := protocol.ReadUInt32(src)
		if err != nil {
			return err
		}
		t.UInt64 = uint64(value)
	case TagTypeUint64:
		value, err := protocol.ReadUInt64(src)
		if err != nil {
			return err
		}
		t.UInt64 = value
	case TagTypeString:
		size, err := protocol.ReadUInt16(src)
		if err != nil {
			return err
		}
		value, err := protocol.ReadBytes(src, int(size))
		if err != nil {
			return err
		}
		t.String = string(value)
	default:
		if t.Type >= TagTypeStr1 && t.Type <= TagTypeStrMax {
			size := int(t.Type-TagTypeStr1) + 1
			value, err := protocol.ReadBytes(src, size)
			if err != nil {
				return err
			}
			t.String = string(value)
			return nil
		}
		return fmt.Errorf("unsupported kad tag type %#x", t.Type)
	}
	return nil
}

func (t Tag) Put(dst *bytes.Buffer) error {
	if t.Name != "" {
		if err := dst.WriteByte(t.Type); err != nil {
			return err
		}
		if err := protocol.WriteUInt16(dst, uint16(len(t.Name))); err != nil {
			return err
		}
		if _, err := dst.WriteString(t.Name); err != nil {
			return err
		}
	} else {
		if err := dst.WriteByte(t.Type | 0x80); err != nil {
			return err
		}
		if err := dst.WriteByte(t.ID); err != nil {
			return err
		}
	}
	switch t.Type {
	case TagTypeHash16:
		_, err := dst.Write(t.Bytes)
		return err
	case TagTypeBool:
		return dst.WriteByte(byte(t.UInt64))
	case TagTypeFloat32:
		return protocol.WriteUInt32(dst, uint32(t.UInt64))
	case TagTypeBoolArray:
		if err := protocol.WriteUInt16(dst, uint16(t.UInt64)); err != nil {
			return err
		}
		_, err := dst.Write(t.Bytes)
		return err
	case TagTypeBlob:
		if err := protocol.WriteUInt32(dst, uint32(len(t.Bytes))); err != nil {
			return err
		}
		_, err := dst.Write(t.Bytes)
		return err
	case TagTypeBsob:
		if err := dst.WriteByte(byte(len(t.Bytes))); err != nil {
			return err
		}
		_, err := dst.Write(t.Bytes)
		return err
	case TagTypeUint8:
		return dst.WriteByte(byte(t.UInt64))
	case TagTypeUint16:
		return protocol.WriteUInt16(dst, uint16(t.UInt64))
	case TagTypeUint32:
		return protocol.WriteUInt32(dst, uint32(t.UInt64))
	case TagTypeUint64:
		return protocol.WriteUInt64(dst, t.UInt64)
	case TagTypeString:
		if err := protocol.WriteUInt16(dst, uint16(len(t.String))); err != nil {
			return err
		}
		_, err := dst.WriteString(t.String)
		return err
	default:
		if t.Type >= TagTypeStr1 && t.Type <= TagTypeStrMax {
			_, err := dst.WriteString(t.String)
			return err
		}
		return fmt.Errorf("unsupported kad tag type %#x", t.Type)
	}
}

type SearchEntry struct {
	ID   ID
	Tags []Tag
}

func (s *SearchEntry) Get(src *bytes.Reader) error {
	if err := s.ID.Get(src); err != nil {
		return err
	}
	count, err := src.ReadByte()
	if err != nil {
		return err
	}
	s.Tags = make([]Tag, int(count))
	for idx := 0; idx < int(count); idx++ {
		if err := s.Tags[idx].Get(src); err != nil {
			return err
		}
	}
	return nil
}

func (s SearchEntry) Put(dst *bytes.Buffer) error {
	if err := s.ID.Put(dst); err != nil {
		return err
	}
	if err := dst.WriteByte(byte(len(s.Tags))); err != nil {
		return err
	}
	for _, tag := range s.Tags {
		if err := tag.Put(dst); err != nil {
			return err
		}
	}
	return nil
}

func (s SearchEntry) UIntTag(id byte) (uint64, bool) {
	for _, tag := range s.Tags {
		if tag.ID == id {
			return tag.UInt64, true
		}
	}
	return 0, false
}

func (s SearchEntry) StringTag(id byte) (string, bool) {
	for _, tag := range s.Tags {
		if tag.ID == id {
			return tag.String, true
		}
	}
	return "", false
}

func (s SearchEntry) SourceEndpoint() (protocol.Endpoint, bool) {
	sourceType, _ := s.UIntTag(TagSourceType)
	ip, hasIP := s.UIntTag(TagSourceIP)
	port, hasPort := s.UIntTag(TagSourcePort)
	if !hasIP || !hasPort {
		return protocol.Endpoint{}, false
	}
	if sourceType != 1 && sourceType != 4 && sourceType != 0 {
		return protocol.Endpoint{}, false
	}
	return protocol.NewEndpoint(int32(uint32(ip)), int(port)), true
}

type SearchSourcesReq struct {
	Target   ID
	StartPos uint16
	Size     uint64
}

func (s *SearchSourcesReq) Unpack(payload []byte) error {
	reader := bytes.NewReader(payload)
	if err := s.Target.Get(reader); err != nil {
		return err
	}
	startPos, err := protocol.ReadUInt16(reader)
	if err != nil {
		return err
	}
	size, err := protocol.ReadUInt64(reader)
	if err != nil {
		return err
	}
	s.StartPos = startPos
	s.Size = size
	return nil
}

func (s SearchSourcesReq) Pack() ([]byte, error) {
	var payload bytes.Buffer
	if err := s.Target.Put(&payload); err != nil {
		return nil, err
	}
	if err := protocol.WriteUInt16(&payload, s.StartPos); err != nil {
		return nil, err
	}
	if err := protocol.WriteUInt64(&payload, s.Size); err != nil {
		return nil, err
	}
	return append([]byte{ProtocolHeader, SearchSrcReqOp}, payload.Bytes()...), nil
}

type SearchRes struct {
	Source  ID
	Target  ID
	Results []SearchEntry
}

func (s *SearchRes) Unpack(payload []byte) error {
	reader := bytes.NewReader(payload)
	if err := s.Source.Get(reader); err != nil {
		return err
	}
	if err := s.Target.Get(reader); err != nil {
		return err
	}
	count, err := protocol.ReadUInt16(reader)
	if err != nil {
		return err
	}
	s.Results = make([]SearchEntry, int(count))
	for idx := 0; idx < int(count); idx++ {
		if err := s.Results[idx].Get(reader); err != nil {
			return err
		}
	}
	return nil
}

func (s SearchRes) Pack() ([]byte, error) {
	var payload bytes.Buffer
	if err := s.Source.Put(&payload); err != nil {
		return nil, err
	}
	if err := s.Target.Put(&payload); err != nil {
		return nil, err
	}
	if err := protocol.WriteUInt16(&payload, uint16(len(s.Results))); err != nil {
		return nil, err
	}
	for _, result := range s.Results {
		if err := result.Put(&payload); err != nil {
			return nil, err
		}
	}
	return encodePacket(SearchResOp, payload.Bytes()), nil
}

type NodesDat struct {
	Version          uint32
	BootstrapEdition uint32
	Contacts         []Entry
}

func (n *NodesDat) Unpack(payload []byte) error {
	reader := bytes.NewReader(payload)
	first, err := protocol.ReadUInt32(reader)
	if err != nil {
		return err
	}
	numContacts := first
	n.Version = 0
	n.BootstrapEdition = 0
	if first == 0 {
		n.Version, err = protocol.ReadUInt32(reader)
		if err != nil {
			return err
		}
		if n.Version >= 1 && n.Version <= 3 {
			if n.Version >= 3 {
				n.BootstrapEdition, err = protocol.ReadUInt32(reader)
				if err != nil {
					return err
				}
			}
			numContacts, err = protocol.ReadUInt32(reader)
			if err != nil {
				return err
			}
		}
	}
	minEntrySize := 16 + 4 + 2 + 2 + 1
	if n.Version >= 2 && n.BootstrapEdition == 0 {
		minEntrySize += 8 + 1
	}
	if minEntrySize <= 0 {
		return errors.New("invalid nodes.dat entry size")
	}
	if uint64(numContacts) > uint64(reader.Len()/minEntrySize) {
		return errors.New("invalid nodes.dat contact count")
	}
	n.Contacts = make([]Entry, 0, numContacts)
	for idx := uint32(0); idx < numContacts; idx++ {
		var entry Entry
		if err := entry.Get(reader); err != nil {
			return err
		}
		if n.Version >= 2 && n.BootstrapEdition == 0 {
			if _, err := protocol.ReadBytes(reader, 8); err != nil {
				return err
			}
			verified, err := reader.ReadByte()
			if err != nil {
				return err
			}
			entry.Verified = verified != 0
		}
		n.Contacts = append(n.Contacts, entry)
	}
	return nil
}

func ParseNodesDat(raw []byte) (*NodesDat, error) {
	nodes := &NodesDat{}
	if err := nodes.Unpack(raw); err != nil {
		return nil, err
	}
	return nodes, nil
}

func LoadNodesDat(path string) (*NodesDat, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseNodesDat(raw)
}

func DecodePacket(packet []byte) (byte, []byte, error) {
	if len(packet) < 2 {
		return 0, nil, errors.New("kad packet too short")
	}
	switch packet[0] {
	case ProtocolHeader:
		return packet[1], packet[2:], nil
	case PackedProtocolHeader:
		// aMule/eMule call CPacket::PackPacket() for KAD datagrams larger
		// than 200 bytes. Only the payload (after header+opcode) is zlib
		// compressed; the opcode stays in the clear.
		reader, err := zlib.NewReader(bytes.NewReader(packet[2:]))
		if err != nil {
			return 0, nil, err
		}
		defer reader.Close()
		const maxKadPayload = 4 << 20
		payload, err := io.ReadAll(io.LimitReader(reader, maxKadPayload))
		if err != nil {
			return 0, nil, err
		}
		if len(payload) == maxKadPayload {
			return 0, nil, errors.New("kad packed payload too large")
		}
		return packet[1], payload, nil
	default:
		return 0, nil, errors.New("unsupported kad protocol header")
	}
}

func DistanceCompare(a, b, target ID) int {
	for i := 0; i < 16; i++ {
		da := a.Hash.At(i) ^ target.Hash.At(i)
		db := b.Hash.At(i) ^ target.Hash.At(i)
		if da < db {
			return -1
		}
		if da > db {
			return 1
		}
	}
	return 0
}
