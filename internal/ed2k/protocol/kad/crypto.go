package kad

// KAD2 (Kademlia version >= 6) UDP obfuscation, compatible with eMule/aMule.
//
// Wire layout of an encrypted datagram:
//
//	byte 0      marker: bit0=0 (Kad), bit1=0 (key=KadID) or 1 (key=receiver verify key)
//	bytes 1..2  random key part (little endian), also part of the MD5 key
//	bytes 3..6  encrypted MAGICVALUE_UDP_SYNC_CLIENT, big endian on the wire
//	byte 7      encrypted padding length (we always use 0)
//	bytes 8..11 encrypted receiver verify key (big endian)
//	bytes 12..15 encrypted sender verify key (big endian)
//	bytes 16..  encrypted original KAD packet (0xE4, opcode, payload...)
//
// RC4 key derivation (aMule CEncryptedDatagramSocket::EncryptSendClient):
//
//	MD5(KadID crypt bytes[16] || randomPart[2])   when the target KadID is known
//	MD5(receiverVerifyKey LE[4] || randomPart[2]) when only the key is known
//
// The first 1024 RC4 bytes are intentionally NOT discarded for UDP keys.

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
	kadUDPHeaderLen = 16 // 8 crypt header + 8 verify keys
	kadMagic        = uint32(0x395F2EC1)
)

func reservedProtocolByte(b byte) bool {
	switch b {
	case 0xE0, 0xE1, 0xE2, 0xE3, ProtocolHeader, 0xE5:
		return true
	default:
		return false
	}
}

func idCryptBytes(id ID) ([]byte, error) {
	var buf bytes.Buffer
	if err := id.Put(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncryptDatagram wraps a plaintext KAD packet (0xE4 + opcode + payload) in
// the Kad2 RC4 envelope. targetID is the destination node's KadID; when it is
// unavailable the encryption key falls back to the receiver verify key.
func EncryptDatagram(plain []byte, targetID ID, receiverVerifyKey, senderVerifyKey uint32) ([]byte, error) {
	if len(plain) < 2 || plain[0] != ProtocolHeader {
		return nil, errors.New("invalid plaintext kad datagram")
	}
	marker := byte(0) // Kad packet, KadID key
	var randomPart [2]byte
	if _, err := rand.Read(randomPart[:]); err != nil {
		return nil, err
	}
	var keyData []byte
	if !targetID.Hash.Equal(protocol.Invalid) {
		raw, err := idCryptBytes(targetID)
		if err != nil {
			return nil, err
		}
		keyData = make([]byte, 18)
		copy(keyData[:16], raw[:16])
		binary.LittleEndian.PutUint16(keyData[16:18], binary.LittleEndian.Uint16(randomPart[:]))
	} else {
		if receiverVerifyKey == 0 {
			return nil, errors.New("kad encryption needs target ID or receiver verify key")
		}
		keyData = make([]byte, 6)
		binary.LittleEndian.PutUint32(keyData[:4], receiverVerifyKey)
		binary.LittleEndian.PutUint16(keyData[4:6], binary.LittleEndian.Uint16(randomPart[:]))
		marker = 0x02 // Kad packet, receiver key
	}
	sum := md5.Sum(keyData)
	cipher, err := rc4.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}

	out := make([]byte, kadUDPHeaderLen+len(plain))
	out[0] = marker
	copy(out[1:3], randomPart[:])
	// Avoid protocol header values in the marker byte; preserve the key-source bit.
	for reservedProtocolByte(out[0]) {
		out[0] = (out[0] + 2) & 0xFE
		out[0] |= marker & 0x02
	}
	scratch := make([]byte, 13+len(plain))
	binary.LittleEndian.PutUint32(scratch[0:4], kadMagic)
	scratch[4] = 0 // padLen
	binary.LittleEndian.PutUint32(scratch[5:9], receiverVerifyKey)
	binary.LittleEndian.PutUint32(scratch[9:13], senderVerifyKey)
	copy(scratch[13:], plain)
	cipher.XORKeyStream(out[3:], scratch)
	return out, nil
}

// DecryptDatagram tries the two Kad key sources (our KadID, our UDP verify
// key). Plaintext datagrams are returned unchanged with ok=false.
func DecryptDatagram(datagram []byte, selfID ID, selfUDPVerifyKey uint32) (plain []byte, senderVerifyKey uint32, ok bool) {
	if len(datagram) <= kadUDPHeaderLen {
		return datagram, 0, false
	}
	if reservedProtocolByte(datagram[0]) {
		return datagram, 0, false // plaintext protocol packet
	}
	randomPart := binary.LittleEndian.Uint16(datagram[1:3])
	attempt := func(keyData []byte) ([]byte, uint32, bool) {
		sum := md5.Sum(keyData)
		cipher, err := rc4.NewCipher(sum[:])
		if err != nil {
			return nil, 0, false
		}
		dec := make([]byte, len(datagram)-3)
		cipher.XORKeyStream(dec, datagram[3:])
		if binary.LittleEndian.Uint32(dec[:4]) != kadMagic {
			return nil, 0, false
		}
		pad := int(dec[4])
		if pad > 15 || len(dec) < 13+pad+2 {
			return nil, 0, false
		}
		// The receiver key proves address ownership to eMule/aMule, but a
		// mismatch must not prevent decoding: most requests are still served,
		// only a few (HELLO_RES_ACK, publish) require a valid receiver key.
		_ = binary.LittleEndian.Uint32(dec[5+pad : 9+pad])
		sender := binary.LittleEndian.Uint32(dec[9+pad : 13+pad])
		return dec[13+pad:], sender, true
	}

	// Key source hinted by the marker: bit1 set means receiver key.
	if datagram[0]&0x02 != 0 && selfUDPVerifyKey != 0 {
		var keyData [6]byte
		binary.LittleEndian.PutUint32(keyData[:4], selfUDPVerifyKey)
		binary.LittleEndian.PutUint16(keyData[4:6], randomPart)
		if p, key, valid := attempt(keyData[:]); valid {
			return p, key, true
		}
	}
	raw, err := idCryptBytes(selfID)
	if err == nil {
		var keyData [18]byte
		copy(keyData[:16], raw[:16])
		binary.LittleEndian.PutUint16(keyData[16:18], randomPart)
		if p, key, valid := attempt(keyData[:]); valid {
			return p, key, true
		}
	}
	return datagram, 0, false
}
