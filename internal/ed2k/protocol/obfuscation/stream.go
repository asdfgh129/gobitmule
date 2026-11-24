// Package obfuscation (stream.go) wires the basic-obfuscation frame codec
// into a live net.Conn. The stream wrapper performs the blocking A/B
// handshake at connect time and then transparently RC4-encrypts writes and
// decrypts reads, so callers can keep using the ordinary net.Conn API.
//
// This is the missing "wired" layer for ED-05: handshake.go alone only
// proves the frame codec is self-consistent; here the same codec drives a
// real byte stream in both directions.
package obfuscation

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// handshakeDeadline bounds the blocking handshake. It is cleared once the
// handshake completes so the wrapped connection keeps its own deadlines.
const handshakeDeadline = 5 * time.Second

// Stream is a net.Conn that transparently applies the negotiated RC4 keys to
// every Read/Write after a completed basic-obfuscation handshake.
type Stream struct {
	raw  net.Conn
	send *Cipher // encrypts our writes
	recv *Cipher // decrypts their writes
}

// NewStream builds a Stream from an already-handshaken connection and its
// directional keys. It is exported so callers that manage the handshake
// themselves can still use the transparent stream layer.
func NewStream(raw net.Conn, send, recv *Cipher) *Stream {
	return &Stream{raw: raw, send: send, recv: recv}
}

func (s *Stream) Read(p []byte) (int, error) {
	n, err := s.raw.Read(p)
	if n > 0 {
		s.recv.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (s *Stream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ct := make([]byte, len(p))
	s.send.XORKeyStream(ct, p)
	n, err := s.raw.Write(ct)
	// Report the plaintext byte count: callers (e.g. Connection.FlushOutgoing)
	// track progress against the original buffer length.
	return n, err
}

func (s *Stream) Close() error                       { return s.raw.Close() }
func (s *Stream) LocalAddr() net.Addr                { return s.raw.LocalAddr() }
func (s *Stream) RemoteAddr() net.Addr               { return s.raw.RemoteAddr() }
func (s *Stream) SetDeadline(t time.Time) error      { return s.raw.SetDeadline(t) }
func (s *Stream) SetReadDeadline(t time.Time) error  { return s.raw.SetReadDeadline(t) }
func (s *Stream) SetWriteDeadline(t time.Time) error { return s.raw.SetWriteDeadline(t) }

// RawConn exposes the underlying transport, for diagnostics.
func (s *Stream) RawConn() net.Conn { return s.raw }

// prefixedConn replays bytes consumed while probing a plaintext connection.
// When WrapIncoming decides the peer sent a plain eD2k header, the already
// read marker byte must be returned to the caller's read stream.
type prefixedConn struct {
	prefix []byte
	raw    net.Conn
}

func (p *prefixedConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.raw.Read(b)
}
func (p *prefixedConn) Write(b []byte) (int, error)   { return p.raw.Write(b) }
func (p *prefixedConn) Close() error                  { return p.raw.Close() }
func (p *prefixedConn) LocalAddr() net.Addr           { return p.raw.LocalAddr() }
func (p *prefixedConn) RemoteAddr() net.Addr          { return p.raw.RemoteAddr() }
func (p *prefixedConn) SetDeadline(t time.Time) error { return p.raw.SetDeadline(t) }
func (p *prefixedConn) SetReadDeadline(t time.Time) error {
	return p.raw.SetReadDeadline(t)
}
func (p *prefixedConn) SetWriteDeadline(t time.Time) error {
	return p.raw.SetWriteDeadline(t)
}

// randomPart returns a non-zero 32-bit value for key derivation. It falls
// back to a time-derived value only if the system CSPRNG is unavailable.
func randomPart() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(time.Now().UnixNano()) | 1
	}
	v := binary.LittleEndian.Uint32(b[:])
	if v == 0 {
		return 1
	}
	return v
}

func randomMarker() byte {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return PickMarker(byte(time.Now().UnixNano()))
	}
	return PickMarker(b[0])
}

// WrapOutgoing performs the client-A (outgoing) handshake on an already
// connected socket. peerHash is the target client's user hash, which is the
// anchor eMule uses for both directional keys. On success the returned conn
// is an encrypted Stream; subsequent I/O is transparent.
func WrapOutgoing(raw net.Conn, peerHash protocol.Hash, padding []byte) (net.Conn, error) {
	_ = raw.SetDeadline(time.Now().Add(handshakeDeadline))
	defer func() { _ = raw.SetDeadline(time.Time{}) }()

	part := randomPart()
	send, recv := DeriveDirectionKeys(peerHash, part, true)
	request, err := InitiatorRequest(randomMarker(), part, padding, send)
	if err != nil {
		return nil, err
	}
	if _, err := raw.Write(request); err != nil {
		return nil, err
	}

	// B replies with an all-encrypted frame: sync(4) + method(1) + padLen(1).
	// Decrypt it incrementally with recv so the keystream stays aligned with
	// the padding bytes that follow.
	head := make([]byte, 6)
	if _, err := io.ReadFull(raw, head); err != nil {
		return nil, err
	}
	plain := make([]byte, len(head))
	recv.XORKeyStream(plain, head)
	if got := binary.LittleEndian.Uint32(plain[0:4]); got != MagicValueSync {
		return nil, ErrBadSync
	}
	if plain[4] != EncryptionObfuscation {
		return nil, ErrBadMethod
	}
	if pad := int(plain[5]); pad > 0 {
		buf := make([]byte, pad)
		if _, err := io.ReadFull(raw, buf); err != nil {
			return nil, err
		}
		recv.XORKeyStream(buf, buf)
	}
	return NewStream(raw, send, recv), nil
}

// WrapIncoming performs the client-B (incoming) handshake on an accepted
// socket. It peeks the first byte: a plain eD2k protocol header means the
// peer is not obfuscated and the original connection (with the probe byte
// replayed) is returned with obfuscated=false. Otherwise the obfuscation
// handshake completes using our own user hash as the key anchor.
func WrapIncoming(raw net.Conn, localHash protocol.Hash, padding []byte) (net.Conn, bool, error) {
	_ = raw.SetDeadline(time.Now().Add(handshakeDeadline))
	defer func() { _ = raw.SetDeadline(time.Time{}) }()

	marker := make([]byte, 1)
	if _, err := io.ReadFull(raw, marker); err != nil {
		return nil, false, err
	}
	if !ValidMarker(marker[0]) {
		// Plain eD2k header (0xE3/0xD4/0xC5): fall back to cleartext.
		return &prefixedConn{prefix: marker, raw: raw}, false, nil
	}

	partBuf := make([]byte, 4)
	if _, err := io.ReadFull(raw, partBuf); err != nil {
		return nil, false, err
	}
	part := binary.LittleEndian.Uint32(partBuf)
	send, recv := DeriveDirectionKeys(localHash, part, false)

	// A's encrypted part: sync(4)+method(1)+method(1)+padLen(1)+padding.
	head := make([]byte, 7)
	if _, err := io.ReadFull(raw, head); err != nil {
		return nil, false, err
	}
	plain := make([]byte, len(head))
	recv.XORKeyStream(plain, head)
	if got := binary.LittleEndian.Uint32(plain[0:4]); got != MagicValueSync {
		return nil, false, ErrBadSync
	}
	if plain[4] != EncryptionObfuscation || plain[5] != EncryptionObfuscation {
		return nil, false, ErrBadMethod
	}
	if pad := int(plain[6]); pad > 0 {
		buf := make([]byte, pad)
		if _, err := io.ReadFull(raw, buf); err != nil {
			return nil, false, err
		}
		recv.XORKeyStream(buf, buf)
	}

	reply, err := ResponderReply(padding, send)
	if err != nil {
		return nil, false, err
	}
	if _, err := raw.Write(reply); err != nil {
		return nil, false, err
	}
	return NewStream(raw, send, recv), true, nil
}
