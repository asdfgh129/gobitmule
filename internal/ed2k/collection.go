package goed2k

// #9: eMule collection (.emulecollection) support.
//
// eMule writes collections in two shapes and CCollection::InitCollectionFromFile
// accepts both:
//
//	text   – one ed2k link per line; lines starting with '#' are comments
//	binary – <uint32 version><uint32 headerTags>…<uint32 fileCount>
//	         <uint32 fileTags>… per file, where every tag is the usual eD2k tag
//	         encoding (type byte, name, value) as parsed by CTag(packets.cpp:472)
//
// The parser keeps what the download engine needs (hash, name, size, AICH root)
// so a collection can be turned into a batch of AddLink calls.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// eD2k tag type ids (opcodes.h TAGTYPE_*).
const (
	tagTypeHash    byte = 0x01
	tagTypeString  byte = 0x02
	tagTypeUint32  byte = 0x03
	tagTypeFloat32 byte = 0x04
	tagTypeBool    byte = 0x05
	tagTypeBoolArr byte = 0x06
	tagTypeBlob    byte = 0x07
	tagTypeUint16  byte = 0x08
	tagTypeUint8   byte = 0x09
	tagTypeBSOB    byte = 0x0A
	tagTypeUint64  byte = 0x0B
)

// eD2k file tag ids used by collections (opcodes.h FT_*).
const (
	ftFileName     uint8 = 0x01
	ftFileSize     uint8 = 0x02
	ftAICHHash     uint8 = 0x27
	ftFileHash     uint8 = 0x28
	ftCollectionAu uint8 = 0x31
	ftFileSizeHi   uint8 = 0x3A
)

// CollectionFile is one entry of a collection.
type CollectionFile struct {
	Hash     protocol.Hash
	Name     string
	Size     int64
	AICHRoot string // base32 AICH root when the collection carries FT_AICH_HASH
}

// Collection is a parsed .emulecollection.
type Collection struct {
	Name   string
	Author string
	Files  []CollectionFile
	// Text reports which on-disk shape was parsed; it is informational and
	// lets callers round-trip the same format.
	Text bool
}

// collectionLinkPattern matches an ed2k file link inside a text collection.
var collectionLinkPattern = regexp.MustCompile(`(?i)ed2k://\|file\|`)

// ParseCollection auto-detects the collection format and parses it.
func ParseCollection(data []byte) (*Collection, error) {
	if len(data) < 8 {
		return nil, errors.New("collection: truncated header")
	}
	version := binary.LittleEndian.Uint32(data[:4])
	if version == 1 || version == 2 {
		return parseBinaryCollection(data)
	}
	return parseTextCollection(data)
}

// ParseCollectionFile reads and parses a collection from disk.
func ParseCollectionFile(path string) (*Collection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	col, err := ParseCollection(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return col, nil
}

// Links renders the collection as ed2k file links, ready for Client.AddLink.
func (c *Collection) Links() []string {
	links := make([]string, 0, len(c.Files))
	for _, f := range c.Files {
		links = append(links, f.Link())
	}
	return links
}

// Link renders one entry as an ed2k file link.
func (f CollectionFile) Link() string {
	link := fmt.Sprintf("ed2k://|file|%s|%d|%s|", f.Name, f.Size, f.Hash.String())
	if f.AICHRoot != "" {
		link += "h=" + f.AICHRoot + "|"
	}
	return link + "/"
}

// parseTextCollection reads the newline separated link list eMule writes when
// "text format" is selected (Collection.cpp WriteToFileAddShared).
func parseTextCollection(data []byte) (*Collection, error) {
	col := &Collection{Text: true}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !collectionLinkPattern.MatchString(line) {
			continue // tolerate stray text such as a collection title line
		}
		link, err := ParseEMuleLink(line)
		if err != nil || link.Type != LinkFile {
			continue // a broken link must not abort the whole collection
		}
		entry := CollectionFile{Hash: link.Hash, Name: link.StringValue, Size: link.NumberValue}
		if len(link.AICHRoot) == 20 {
			entry.AICHRoot = encodeBase32(link.AICHRoot)
		}
		col.Files = append(col.Files, entry)
	}
	if len(col.Files) == 0 {
		return nil, errors.New("collection: no ed2k links found")
	}
	return col, nil
}

// parseBinaryCollection walks the binary layout used by eMule 0.4x+.
func parseBinaryCollection(data []byte) (*Collection, error) {
	r := &tagReader{data: data, pos: 4} // skip the version word
	col := &Collection{}

	headerTags, err := r.uint32()
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < headerTags; i++ {
		t, err := r.readTag()
		if err != nil {
			return nil, err
		}
		switch t.nameID {
		case ftFileName:
			if t.str != "" {
				col.Name = t.str
			}
		case ftCollectionAu:
			col.Author = t.str
		}
	}

	fileCount, err := r.uint32()
	if err != nil {
		return nil, err
	}
	if fileCount > 100000 {
		return nil, fmt.Errorf("collection: implausible file count %d", fileCount)
	}
	for i := uint32(0); i < fileCount; i++ {
		tagCount, err := r.uint32()
		if err != nil {
			return nil, err
		}
		var entry CollectionFile
		var sizeLow, sizeHigh uint64
		var hash protocol.Hash
		for j := uint32(0); j < tagCount; j++ {
			t, err := r.readTag()
			if err != nil {
				return nil, err
			}
			switch t.nameID {
			case ftFileName:
				entry.Name = t.str
			case ftFileSize:
				sizeLow = t.num
			case ftFileSizeHi:
				sizeHigh = t.num
			case ftFileHash:
				if len(t.hash) == 16 {
					if parsed, err := protocol.HashFromBytes(t.hash); err == nil {
						hash = parsed
					}
				}
			case ftAICHHash:
				entry.AICHRoot = t.str
			}
		}
		if hash.IsZero() {
			continue // a collection entry without a hash cannot be downloaded
		}
		entry.Hash = hash
		entry.Size = parseCollectionSize(sizeLow, sizeHigh)
		col.Files = append(col.Files, entry)
	}
	if len(col.Files) == 0 {
		return nil, errors.New("collection: no files in binary collection")
	}
	return col, nil
}

// tagReader is a bounds-checked little-endian cursor over tag encoded data.
type tagReader struct {
	data []byte
	pos  int
}

func (r *tagReader) remaining() int { return len(r.data) - r.pos }

func (r *tagReader) bytes(n int) ([]byte, error) {
	if n < 0 || r.remaining() < n {
		return nil, errors.New("collection: truncated tag data")
	}
	out := r.data[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *tagReader) uint8() (uint8, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *tagReader) uint16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func (r *tagReader) uint32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *tagReader) uint64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// readString mirrors CFileDataIO::ReadString: uint16 length, then that many
// bytes; a zero length is the empty string.
func (r *tagReader) readString() (string, error) {
	n, err := r.uint16()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	b, err := r.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// tag is a decoded eD2k tag. Only the fields the collection parser consumes are
// kept; the reader still walks unknown values so trailing tags stay aligned.
type tag struct {
	nameID uint8
	name   string
	str    string
	num    uint64
	hash   []byte
}

// readTag decodes one tag exactly as CTag(packets.cpp:472) does.
func (r *tagReader) readTag() (tag, error) {
	var t tag
	typ, err := r.uint8()
	if err != nil {
		return t, err
	}
	if typ&0x80 != 0 {
		typ &= 0x7F
		id, err := r.uint8()
		if err != nil {
			return t, err
		}
		t.nameID = id
	} else {
		length, err := r.uint16()
		if err != nil {
			return t, err
		}
		if length == 1 {
			id, err := r.uint8()
			if err != nil {
				return t, err
			}
			t.nameID = id
		} else if length > 0 {
			name, err := r.bytes(int(length))
			if err != nil {
				return t, err
			}
			t.name = string(name)
		}
	}

	switch {
	case typ == tagTypeString:
		t.str, err = r.readString()
	case typ == tagTypeUint32:
		var v uint32
		v, err = r.uint32()
		t.num = uint64(v)
	case typ == tagTypeUint64:
		t.num, err = r.uint64()
	case typ == tagTypeUint16:
		var v uint16
		v, err = r.uint16()
		t.num = uint64(v)
	case typ == tagTypeUint8:
		var v uint8
		v, err = r.uint8()
		t.num = uint64(v)
	case typ == tagTypeFloat32:
		_, err = r.bytes(4)
	case typ >= 0x11 && typ <= 0x24:
		// TAGTYPE_STR1..STR20 carry an inline string of exactly that length.
		t.str, err = r.fixedString(int(typ-0x11) + 1)
	case typ == tagTypeHash:
		t.hash, err = r.bytes(16)
	case typ == tagTypeBool:
		_, err = r.bytes(1)
	case typ == tagTypeBoolArr:
		var bits uint16
		bits, err = r.uint16()
		if err == nil {
			// eMule seeks (len/8)+1 bytes after the length word.
			_, err = r.bytes(int(bits)/8 + 1)
		}
	case typ == tagTypeBlob:
		var size uint32
		size, err = r.uint32()
		if err == nil {
			_, err = r.bytes(int(size))
		}
	default:
		// Unknown tag types make the stream ambiguous; stop instead of
		// silently desynchronising the remaining entries.
		return t, fmt.Errorf("collection: unsupported tag type 0x%02X", typ)
	}
	return t, err
}

// fixedString reads a TAGTYPE_STRn value: uint16 length (may be shorter than n)
// followed by the bytes.
func (r *tagReader) fixedString(max int) (string, error) {
	n, err := r.uint16()
	if err != nil {
		return "", err
	}
	if int(n) > max {
		return "", errors.New("collection: inline string longer than its tag type")
	}
	b, err := r.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// encodeBase32 renders an AICH root the way eMule's FT_AICH_HASH does
// (CBase32 encoding, uppercase, no padding).
func encodeBase32(raw []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var out strings.Builder
	var buffer uint
	var bits uint
	for _, b := range raw {
		buffer = buffer<<8 | uint(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out.WriteByte(alphabet[(buffer>>bits)&0x1F])
		}
	}
	if bits > 0 {
		out.WriteByte(alphabet[(buffer<<(5-bits))&0x1F])
	}
	return out.String()
}

// decodeBase32 parses the AICH root form used on ed2k links (h=).
func decodeBase32(s string) ([]byte, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var out []byte
	var buffer uint
	var bits uint
	for i := 0; i < len(s); i++ {
		idx := strings.IndexByte(alphabet, s[i])
		if idx < 0 {
			return nil, fmt.Errorf("invalid base32 character %q", s[i])
		}
		buffer = buffer<<5 | uint(idx)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(buffer>>bits))
		}
	}
	return out, nil
}

// parseCollectionSize combines FT_FILESIZE and FT_FILESIZE_HI.
func parseCollectionSize(low, high uint64) int64 {
	if high > 0 {
		return int64(high<<32 | low&0xFFFFFFFF)
	}
	return int64(low)
}
