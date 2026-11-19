package hlstransport

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// decryptSegment decrypts one AES-128 HLS media segment in place semantics:
// the whole segment payload is a single AES-128 CBC blob with PKCS7 padding
// (RFC 8216 §4.3.2.4: "Media Segments ... encrypted using AES-128 CBC with
// PKCS7 padding"). IV precedence (§5.2): the EXT-X-KEY IV attribute, else the
// 128-bit big-endian media sequence number of the segment.
func decryptSegment(seg Segment, mediaSeq int64, keyBytes []byte, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, ErrDecryptFailed
	}
	if len(body)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: ciphertext is %d bytes, not a multiple of 16", ErrDecryptFailed, len(body))
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecryptFailed, err)
	}
	iv, err := keyIV(seg, mediaSeq)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, body)
	plain, err = pkcs7Unpad(plain)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// keyIV resolves the IV for a segment: the explicit EXT-X-KEY IV attribute
// (hex, with or without 0x prefix) or the media sequence number as a
// 128-bit big-endian integer.
func keyIV(seg Segment, mediaSeq int64) ([]byte, error) {
	if seg.Key != nil && seg.Key.IV != "" {
		raw, err := hex.DecodeString(seg.Key.IV)
		if err != nil || len(raw) != aes.BlockSize {
			return nil, fmt.Errorf("%w: IV must be 32 hex digits", ErrDecryptFailed)
		}
		return raw, nil
	}
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], uint64(mediaSeq))
	return iv[:], nil
}

// pkcs7Unpad validates and removes the padding. A bad pad byte means the key
// or IV was wrong — fail loudly instead of handing corrupt bytes to the file.
func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrDecryptFailed
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(b) {
		return nil, fmt.Errorf("%w: invalid PKCS7 padding byte %d", ErrDecryptFailed, pad)
	}
	for _, c := range b[len(b)-pad:] {
		if int(c) != pad {
			return nil, fmt.Errorf("%w: inconsistent PKCS7 padding", ErrDecryptFailed)
		}
	}
	return b[:len(b)-pad], nil
}

// sequenceOf returns the media sequence number of the segment at index i.
func sequenceOf(pl *MediaPlaylist, i int) int64 {
	return pl.MediaSeq + int64(i)
}

// parseHexKey decodes the 16-byte AES key fetched from the EXT-X-KEY URI.
// Some CDNs wrap the key in whitespace; trim before decoding.
func parseHexKey(raw string) ([]byte, error) {
	b, err := hex.DecodeString(trimASCII(raw))
	if err != nil || len(b) != 16 {
		return nil, fmt.Errorf("%w: key must be 32 hex digits / 16 bytes", ErrKeyFetchFailed)
	}
	return b, nil
}

func trimASCII(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\r' || s[start] == '\n' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\r' || s[end-1] == '\n' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
