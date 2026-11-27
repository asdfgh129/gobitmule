package server

// #2: eD2k server UDP datagrams. eMule sends global searches and source
// requests over UDP (opcodes 0x90/0x9A) so a server can answer without a TCP
// session, and optionally wraps them in the server-key RC4 envelope described
// in CEncryptedDatagramSocket::EncryptSendServer/DecryptReceivedServer.

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

const (
	OpGlobSearchReq    byte = 0x90
	OpGlobSearchRes    byte = 0x99
	OpGlobGetSources2  byte = 0x94 // <hash 16><filesize 4>
	OpGlobGetSources   byte = 0x9A // <hash 16>
	OpGlobFoundSources byte = 0x9B

	// Server-encrypted datagram constants (eMule EncryptedDatagramSocket.cpp).
	magicUDPSyncServer        = uint32(0x13EF24D5)
	magicValueUDPServerClient = byte(0xA5) // MAGICVALUE_UDP_SERVERCLIENT
	magicValueUDPClientServer = byte(0x6B) // MAGICVALUE_UDP_CLIENTSERVER
	cryptHeaderWithoutPadding = 8
)

// UDPGlobGetSources packs OP_GLOBGETSOURCES2: <hash 16><filesize 4>.
func UDPGlobGetSources(hash protocol.Hash, size int64) []byte {
	var body bytes.Buffer
	_ = protocol.WriteHash(&body, hash)
	_ = binary.Write(&body, binary.LittleEndian, uint32(size))
	return packUDPDatagram(OpGlobGetSources2, body.Bytes())
}

// UDPGlobSearchReq packs OP_GLOBSEARCHREQ3: an eMule tag-set search expression.
// The query bytes are the same wire form as a TCP SearchRequest body.
func UDPGlobSearchReq(req *SearchRequest) ([]byte, error) {
	if req == nil {
		return nil, errors.New("nil search request")
	}
	var body bytes.Buffer
	if err := req.Put(&body); err != nil {
		return nil, err
	}
	return packUDPDatagram(OpGlobSearchReq, body.Bytes()), nil
}

func packUDPDatagram(opcode byte, body []byte) []byte {
	out := make([]byte, 2+len(body))
	out[0] = protocol.EdonkeyHeader
	out[1] = opcode
	copy(out[2:], body)
	return out
}

// ParseUDPFoundSources decodes OP_GLOBFOUNDSOURCES: one or more records of
// <hash 16><count 1><ip 4><port 2>... concatenated in a single datagram.
func ParseUDPFoundSources(datagram []byte) map[protocol.Hash][]protocol.Endpoint {
	result := make(map[protocol.Hash][]protocol.Endpoint)
	body, ok := stripUDPDatagram(datagram, OpGlobFoundSources)
	if !ok {
		return result
	}
	r := bytes.NewReader(body)
	for r.Len() > 0 {
		hash, err := protocol.ReadHash(r)
		if err != nil {
			break
		}
		countByte, err := r.ReadByte()
		if err != nil {
			break
		}
		for i := 0; i < int(countByte); i++ {
			ip, err := protocol.ReadInt32(r)
			if err != nil {
				return result
			}
			port, err := protocol.ReadUInt16(r)
			if err != nil {
				return result
			}
			result[hash] = append(result[hash], protocol.NewEndpoint(ip, int(port)))
		}
	}
	return result
}

// ParseUDPSearchResults decodes OP_GLOBSEARCHRES: a sequence of
// SharedFileEntry records (hash/clientID/port/tags) with no count prefix.
func ParseUDPSearchResults(datagram []byte) ([]SharedFileEntry, error) {
	body, ok := stripUDPDatagram(datagram, OpGlobSearchRes)
	if !ok {
		return nil, errors.New("not a glob search result datagram")
	}
	r := bytes.NewReader(body)
	entries := make([]SharedFileEntry, 0, 8)
	for r.Len() > 0 {
		var entry SharedFileEntry
		if err := entry.Get(r); err != nil {
			break
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func stripUDPDatagram(datagram []byte, opcode byte) ([]byte, bool) {
	// Skip any leading encrypted header is handled by DecryptServerDatagram.
	for len(datagram) >= 2 {
		if datagram[0] == protocol.EdonkeyHeader && datagram[1] == opcode {
			return datagram[2:], true
		}
		datagram = datagram[1:]
	}
	return nil, false
}

// EncryptServerDatagram wraps a plaintext eD2k datagram in the eMule server-key
// RC4 envelope: [marker][random2][enc magic4][enc padlen1][enc payload...].
func EncryptServerDatagram(plain []byte, baseKey uint32) ([]byte, error) {
	if baseKey == 0 || len(plain) == 0 {
		return nil, errors.New("server UDP encryption needs a base key and payload")
	}
	var randomPart [2]byte
	if _, err := rand.Read(randomPart[:]); err != nil {
		return nil, err
	}
	keyData := make([]byte, 7)
	binary.LittleEndian.PutUint32(keyData[:4], baseKey)
	keyData[4] = magicValueUDPClientServer
	copy(keyData[5:7], randomPart[:])
	sum := md5.Sum(keyData)
	cipher, err := rc4.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, cryptHeaderWithoutPadding+len(plain))
	marker := byte(0x01) // never a protocol header value
	for _, reserved := range []byte{protocol.EdonkeyHeader, 0xE4, 0xE5, 0xD4, 0xC5} {
		if marker == reserved {
			marker = 0x02
		}
	}
	out[0] = marker
	copy(out[1:3], randomPart[:])
	var scratch bytes.Buffer
	_ = binary.Write(&scratch, binary.LittleEndian, magicUDPSyncServer)
	_ = scratch.WriteByte(0) // padLen = 0, as eMule does for UDP
	scratch.Write(plain)
	cipher.XORKeyStream(out[3:], scratch.Bytes())
	return out, nil
}

// DecryptServerDatagram reverses EncryptServerDatagram. Plaintext datagrams
// are returned unchanged with ok=false.
func DecryptServerDatagram(datagram []byte, baseKey uint32) (plain []byte, ok bool) {
	if baseKey == 0 || len(datagram) <= cryptHeaderWithoutPadding || datagram[0] == protocol.EdonkeyHeader {
		return datagram, false
	}
	randomPart := datagram[1:3]
	// Direction magic distinguishes client->server (CLIENTSERVER) from
	// server->client (SERVERCLIENT) envelopes. Try both so datagrams captured
	// from either side decode with the same shared base key.
	for _, direction := range []byte{magicValueUDPServerClient, magicValueUDPClientServer} {
		keyData := make([]byte, 7)
		binary.LittleEndian.PutUint32(keyData[:4], baseKey)
		keyData[4] = direction
		copy(keyData[5:7], randomPart)
		sum := md5.Sum(keyData)
		cipher, err := rc4.NewCipher(sum[:])
		if err != nil {
			return datagram, false
		}
		dec := make([]byte, len(datagram)-3)
		cipher.XORKeyStream(dec, datagram[3:])
		if len(dec) < 5 || binary.LittleEndian.Uint32(dec[:4]) != magicUDPSyncServer {
			continue
		}
		pad := int(dec[4] & 0x0f)
		if len(dec) < 5+pad {
			continue
		}
		return dec[5+pad:], true
	}
	return datagram, false
}
