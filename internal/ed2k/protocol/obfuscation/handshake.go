package obfuscation

import (
	"encoding/binary"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

var (
	ErrShortFrame   = errors.New("obfuscation: handshake frame too short")
	ErrBadMarker    = errors.New("obfuscation: semi-random marker equals a protocol header")
	ErrBadSync      = errors.New("obfuscation: sync token mismatch")
	ErrBadMethod    = errors.New("obfuscation: unsupported encryption method")
	ErrPaddingRange = errors.New("obfuscation: padding length does not match frame")
)

// nonProtocolMarkers are the eD2k protocol header bytes that the first
// cleartext byte of an obfuscated stream must never equal.
var nonProtocolMarkers = [3]byte{
	protocol.EdonkeyHeader, // 0xE3 OP_EDONKEYPROT
	protocol.PackedProt,    // 0xD4 OP_PACKEDPROT
	protocol.EMuleProt,     // 0xC5 OP_EMULEPROT
}

// ValidMarker reports whether b can be used as the semi-random, non-protocol
// marker that prefixes an obfuscated stream.
func ValidMarker(b byte) bool {
	for _, m := range nonProtocolMarkers {
		if b == m {
			return false
		}
	}
	return true
}

// PickMarker returns b if it is a valid marker, otherwise the first valid
// non-protocol byte starting from b+1. It never returns an invalid marker.
func PickMarker(b byte) byte {
	for tries := 0; tries < 256; tries++ {
		if ValidMarker(b) {
			return b
		}
		b++
	}
	return 0x01 // unreachable: 0x01 is never an eD2k header
}

// InitiatorRequest builds the handshake frame that client A sends.
//
// Layout (A send key encrypts from byte 5):
//
//	[0]     semi-random non-protocol marker   (cleartext)
//	[1..4]  random key part (uint32 LE)       (cleartext)
//	[5..8]  MAGICVALUE_SYNC                   (encrypted)
//	[9]     supported method (0)              (encrypted)
//	[10]    preferred method (0)              (encrypted)
//	[11]    padding length                    (encrypted)
//	[12..]  padding bytes                     (encrypted)
//
// randomPart is chosen by A; send is A's send Cipher (targetHash || 0x22 || part).
func InitiatorRequest(marker byte, randomPart uint32, padding []byte, send *Cipher) ([]byte, error) {
	if !ValidMarker(marker) {
		return nil, ErrBadMarker
	}
	if len(padding) > 255 {
		return nil, ErrPaddingRange
	}
	plain := make([]byte, 7+len(padding))
	binary.LittleEndian.PutUint32(plain[0:4], MagicValueSync)
	plain[4] = EncryptionObfuscation
	plain[5] = EncryptionObfuscation
	plain[6] = byte(len(padding))
	copy(plain[7:], padding)

	frame := make([]byte, 5+len(plain))
	frame[0] = marker
	binary.LittleEndian.PutUint32(frame[1:5], randomPart)
	send.XORKeyStream(frame[5:], plain)
	return frame, nil
}

// ParseInitiatorRequest parses A's frame from B's side. recv is B's receive
// Cipher (localHash || 0x22 || randomPart). It returns A's random key part and
// the padding length carried in the frame.
func ParseInitiatorRequest(frame []byte, recv *Cipher) (randomPart uint32, paddingLen byte, err error) {
	if len(frame) < 12 {
		return 0, 0, ErrShortFrame
	}
	if !ValidMarker(frame[0]) {
		return 0, 0, ErrBadMarker
	}
	randomPart = binary.LittleEndian.Uint32(frame[1:5])
	ciphertext := frame[5:]
	if len(ciphertext) < 7 {
		return 0, 0, ErrShortFrame
	}
	plain := make([]byte, len(ciphertext))
	recv.XORKeyStream(plain, ciphertext)
	if got := binary.LittleEndian.Uint32(plain[0:4]); got != MagicValueSync {
		return 0, 0, ErrBadSync
	}
	if plain[4] != EncryptionObfuscation || plain[5] != EncryptionObfuscation {
		return 0, 0, ErrBadMethod
	}
	paddingLen = plain[6]
	if int(paddingLen) != len(plain)-7 {
		return 0, 0, ErrPaddingRange
	}
	return randomPart, paddingLen, nil
}

// ResponderReply builds B's reply frame. Every byte is encrypted with B's
// send Cipher (localHash || 0xCB || randomPart):
//
//	[0..3]  MAGICVALUE_SYNC
//	[4]     selected method (0)
//	[5]     padding length
//	[6..]   padding bytes
func ResponderReply(padding []byte, send *Cipher) ([]byte, error) {
	if len(padding) > 255 {
		return nil, ErrPaddingRange
	}
	plain := make([]byte, 6+len(padding))
	binary.LittleEndian.PutUint32(plain[0:4], MagicValueSync)
	plain[4] = EncryptionObfuscation
	plain[5] = byte(len(padding))
	copy(plain[6:], padding)
	frame := make([]byte, len(plain))
	send.XORKeyStream(frame, plain)
	return frame, nil
}

// ParseResponderReply parses B's reply from A's side. recv is A's receive
// Cipher (targetHash || 0xCB || randomPart). It returns the padding length.
func ParseResponderReply(frame []byte, recv *Cipher) (paddingLen byte, err error) {
	if len(frame) < 6 {
		return 0, ErrShortFrame
	}
	plain := make([]byte, len(frame))
	recv.XORKeyStream(plain, frame)
	if got := binary.LittleEndian.Uint32(plain[0:4]); got != MagicValueSync {
		return 0, ErrBadSync
	}
	if plain[4] != EncryptionObfuscation {
		return 0, ErrBadMethod
	}
	paddingLen = plain[5]
	if int(paddingLen) != len(plain)-6 {
		return 0, ErrPaddingRange
	}
	return paddingLen, nil
}
