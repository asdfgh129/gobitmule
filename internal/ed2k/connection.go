package goed2k

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/internal/logx"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol/obfuscation"
)

// connectionIODeadlineNS is the write deadline passed directly to
// net.Conn.SetWriteDeadline as a time.Duration in nanoseconds. It must be
// seconds: a large compressed part can take a while to hand to a slow peer.
const connectionIODeadlineNS = int64(5 * 1000 * 1000 * 1000)

// connectionPollDeadlineNS bounds a single read in PumpIO. PumpIO serves all
// sockets in one loop, so a long idle read would stall every other peer - in
// particular the accepted server callback socket waiting for our Hello. A
// timeout just returns control to the pump; data that arrives later is picked
// up on the next tick, so replies after 200-400ms are not lost.
const connectionPollDeadlineNS = int64(50 * 1000 * 1000)

// incomingProbeWait bounds how long we wait for the first bytes of an
// accepted socket before deciding it is a silent eD2k server callback. A
// server testing our listening port connects and waits for us to send a
// client Hello; it may not send a complete (or even any) frame first. Real
// peers send either a plain eD2k header or the five-byte obfuscation prefix
// immediately, so a short wait keeps both paths intact.
var incomingProbeWait = 750 * time.Millisecond

type Connection struct {
	socket        net.Conn
	session       *Session
	stat          Statistics
	lastReceive   int64
	disconnecting bool
	closeCode     BaseErrorCode
	closeHandled  bool
	outgoing      []queuedPacket
	incoming      [][]byte
	header        protocol.PacketHeader
	headerBuffer  []byte
	bodyBuffer    []byte
	// obfPeerHash, when defined, enables an outgoing eMule basic-obfuscation
	// handshake on Connect. obfuscated/outgoingObfuscation record the live
	// stream state for diagnostics and for CryptLayerWired reporting.
	obfPeerHash         protocol.Hash
	obfHashTentative    bool
	obfuscated          bool
	outgoingObfuscation bool
}

type queuedPacket struct {
	data          []byte
	protocolBytes int64
	payloadBytes  int64
}

func NewConnection(session *Session) Connection {
	return Connection{
		session:      session,
		stat:         NewStatistics(),
		lastReceive:  CurrentTime(),
		closeCode:    NoError,
		outgoing:     make([]queuedPacket, 0),
		incoming:     make([][]byte, 0),
		headerBuffer: make([]byte, 0, protocol.PacketHeaderSize),
		bodyBuffer:   nil,
	}
}

func (c *Connection) DoRead() error {
	if c.socket == nil {
		return nil
	}
	tmp := make([]byte, 16*1024)
	_ = c.socket.SetReadDeadline(CurrentTimeToDeadline(connectionPollDeadlineNS))
	n, err := c.socket.Read(tmp)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil
		}
		if err == io.EOF {
			c.Close(EndOfStream)
			return nil
		}
		c.Close(IOException)
		return err
	}
	if n == 0 {
		return nil
	}
	logx.Debug("socket read", "endpoint", c.Endpoint().String(), "bytes", n)
	c.lastReceive = CurrentTime()
	c.stat.ReceiveBytes(int64(n), 0)
	c.incoming = append(c.incoming, bytes.Clone(tmp[:n]))
	return nil
}

// SetObfuscationPeerHash configures an outgoing eMule basic-obfuscation
// 握手 for this connection. It must be called before Connect. A zero hash
// leaves the connection in plaintext mode. Passing an invalid hash disables
// it. The peer hash is the key anchor eMule uses on the outgoing side.
func (c *Connection) SetObfuscationPeerHash(hash protocol.Hash) {
	if hash.Equal(protocol.Invalid) {
		c.obfPeerHash = protocol.Invalid
		c.obfHashTentative = false
		return
	}
	c.obfPeerHash = hash
	c.obfHashTentative = false
}

// armTentativeObfuscation arms the handshake with a hash inferred from source
// metadata (e.g. a KAD source ID). Unlike an explicit hash, a failed handshake
// is not fatal: Connect retries on a fresh cleartext socket. This keeps
// interoperability with clients whose KAD ID is not their user hash.
func (c *Connection) armTentativeObfuscation(hash protocol.Hash) {
	if hash.Equal(protocol.Invalid) {
		return
	}
	c.obfPeerHash = hash
	c.obfHashTentative = true
}

// IsObfuscated reports whether this connection's TCP stream carries eMule
// basic obfuscation (handshake completed, RC4 active).
func (c *Connection) IsObfuscated() bool { return c.obfuscated }

// incomingPrefixConn replays bytes consumed by the silent-probe peek so the
// obfuscation handshake (or the plaintext parser) still sees the original
// stream from byte zero.
type incomingPrefixConn struct {
	prefix []byte
	raw    net.Conn
}

func (p *incomingPrefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.raw.Read(b)
}
func (p *incomingPrefixConn) Write(b []byte) (int, error)   { return p.raw.Write(b) }
func (p *incomingPrefixConn) Close() error                  { return p.raw.Close() }
func (p *incomingPrefixConn) LocalAddr() net.Addr           { return p.raw.LocalAddr() }
func (p *incomingPrefixConn) RemoteAddr() net.Addr          { return p.raw.RemoteAddr() }
func (p *incomingPrefixConn) SetDeadline(t time.Time) error { return p.raw.SetDeadline(t) }
func (p *incomingPrefixConn) SetReadDeadline(t time.Time) error {
	return p.raw.SetReadDeadline(t)
}
func (p *incomingPrefixConn) SetWriteDeadline(t time.Time) error {
	return p.raw.SetWriteDeadline(t)
}

// peekIncomingPrefix reads up to n bytes without discarding them. A timeout
// with zero bytes means the peer is waiting for us to speak first; a timeout
// after a partial prefix means the probe did not send a usable handshake.
func peekIncomingPrefix(raw net.Conn, n int) ([]byte, bool, error) {
	_ = raw.SetReadDeadline(time.Now().Add(incomingProbeWait))
	defer func() { _ = raw.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, n)
	total := 0
	for total < n {
		read, err := raw.Read(buf[total:])
		total += read
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return buf[:total], true, nil
		}
		if err == io.EOF && total > 0 {
			return buf[:total], true, nil
		}
		return buf[:total], false, err
	}
	return buf[:total], false, nil
}

// prepareIncoming classifies an accepted socket before it is published to the
// IO pump. It returns greeting=true when the peer stayed silent (or sent only
// an incomplete handshake prefix): that is the eD2k server port callback,
// which expects us to send the first client Hello. Plaintext peers and real
// obfuscated peers keep their original paths.
func (c *Connection) prepareIncoming(localHash protocol.Hash, obfEnabled bool) (bool, error) {
	if c.socket == nil {
		return false, nil
	}
	endpoint := c.Endpoint().String()
	prefix, timedOut, err := peekIncomingPrefix(c.socket, 5)
	if err != nil {
		return false, err
	}
	if len(prefix) == 0 && timedOut {
		debugPeerf("incoming %s silent probe, answer with Hello", endpoint)
		return true, nil
	}
	logx.Debug("incoming peer probe", "endpoint", endpoint, "bytes", len(prefix), "prefix", fmt.Sprintf("%X", prefix))

	wrapped := net.Conn(&incomingPrefixConn{prefix: prefix, raw: c.socket})
	if !obfuscation.ValidMarker(prefix[0]) {
		// Plain eD2k header (0xE3/0xD4/0xC5): fall back to cleartext.
		c.socket = wrapped
		return false, nil
	}
	if timedOut || len(prefix) < 5 {
		// A lone marker byte is not a usable obfuscation handshake. Treat it
		// as a port probe and let the caller send a plaintext Hello.
		debugPeerf("incoming %s incomplete probe prefix %X, answer with Hello", endpoint, prefix)
		return true, nil
	}
	if !obfEnabled || localHash.Equal(protocol.Invalid) {
		c.socket = wrapped
		return false, nil
	}
	final, obf, err := obfuscation.WrapIncoming(wrapped, localHash, nil)
	if err != nil {
		return false, err
	}
	c.socket = final
	c.obfuscated = obf
	return false, nil
}

// enableIncomingObfuscation is the obfuscation-enabled entry point kept for
// the existing connection tests.
func (c *Connection) enableIncomingObfuscation(localHash protocol.Hash) (bool, error) {
	return c.prepareIncoming(localHash, true)
}

// connectionDialTimeoutMS bounds a single outbound TCP connect. Source
// candidates are dialed from the session loop, so an unbounded dial to an
// unreachable source froze PumpIO - and with it every live peer's handshake and
// download - until the OS SYN retry gave up (~21s per candidate). The bound is
// deliberately generous because the dial now runs off-loop: a distant holding
// peer often needs more than 5s to complete the TCP handshake.
const connectionDialTimeoutMS = int64(15000)

func (c *Connection) dialTimeout() time.Duration {
	if c.session != nil {
		if secs := c.session.settings.PeerConnectionTimeout; secs > 0 {
			if floor := time.Duration(connectionDialTimeoutMS) * time.Millisecond; time.Duration(secs)*time.Second < floor {
				return floor
			}
			return time.Duration(secs) * time.Second
		}
	}
	return time.Duration(connectionDialTimeoutMS) * time.Millisecond
}

func (c *Connection) dialTCP(timeout time.Duration, addr *net.TCPAddr) (net.Conn, error) {
	if timeout <= 0 {
		return net.DialTCP("tcp", nil, addr)
	}
	return net.DialTimeout("tcp", addr.String(), timeout)
}

// openSocket dials the peer and negotiates optional obfuscation without
// touching receiver state, so a caller can run it on another goroutine and
// publish the result from the session loop.
func (c *Connection) openSocket(address net.Addr) (net.Conn, bool, error) {
	tcpAddr, ok := address.(*net.TCPAddr)
	if !ok {
		return nil, false, nil
	}
	timeout := c.dialTimeout()
	conn, err := c.dialTCP(timeout, tcpAddr)
	if err != nil {
		return nil, false, err
	}
	if c.obfPeerHash.Equal(protocol.Invalid) {
		return conn, false, nil
	}
	wrapped, err := obfuscation.WrapOutgoing(conn, c.obfPeerHash, nil)
	if err != nil {
		_ = conn.Close()
		if c.obfHashTentative {
			// The inferred hash may not be the peer's real user hash
			// (KAD ID semantics differ across clients/versions). Retry
			// once in plaintext rather than dropping a usable source.
			logx.Debug("tentative outgoing obfuscation failed, falling back to plaintext",
				"endpoint", tcpAddr.String(), "error", err.Error())
			clearConn, dialErr := c.dialTCP(timeout, tcpAddr)
			if dialErr != nil {
				return nil, false, dialErr
			}
			return clearConn, false, nil
		}
		return nil, false, NewError(EncryptionError)
	}
	return wrapped, true, nil
}

func (c *Connection) Connect(address net.Addr) error {
	socket, obfuscated, err := c.openSocket(address)
	if err != nil {
		c.Close(errorCodeOf(err))
		return err
	}
	if socket == nil {
		return nil
	}
	c.adoptSocket(socket, obfuscated)
	return nil
}

// adoptSocket installs a freshly dialed socket. It runs on the session loop so
// the rest of the peer state stays single-threaded.
func (c *Connection) adoptSocket(socket net.Conn, obfuscated bool) {
	c.socket = socket
	c.obfuscated = obfuscated
	if obfuscated {
		c.outgoingObfuscation = true
	}
	c.lastReceive = CurrentTime()
}

func errorCodeOf(err error) BaseErrorCode {
	if e, ok := err.(*JED2KError); ok {
		return e.ErrorCode()
	}
	return IOException
}

func (c *Connection) Close(ec BaseErrorCode) {
	if c.disconnecting {
		return
	}
	if c.socket != nil {
		_ = c.socket.Close()
		c.socket = nil
	}
	c.disconnecting = true
	c.closeCode = ec
}

func (c *Connection) SecondTick(tickIntervalMS int64) {
	c.stat.SecondTick(tickIntervalMS)
}

func (c *Connection) Statistics() Statistics {
	return c.stat
}

func (c *Connection) IsDisconnecting() bool {
	return c.disconnecting
}

func (c *Connection) DisconnectCode() BaseErrorCode {
	return c.closeCode
}

func (c *Connection) IsDisconnectHandled() bool {
	return c.closeHandled
}

func (c *Connection) MarkDisconnectHandled() {
	c.closeHandled = true
}

func (c *Connection) MillisecondsSinceLastReceive() int64 {
	return CurrentTime() - c.lastReceive
}

func (c *Connection) Endpoint() protocol.Endpoint {
	return protocol.Endpoint{}
}

func (c *Connection) QueuePacket(packet []byte) {
	c.QueuePacketWithStats(packet, int64(len(packet)), 0)
}

func (c *Connection) QueuePacketWithStats(packet []byte, protocolBytes, payloadBytes int64) {
	if len(packet) == 0 {
		return
	}
	buf := bytes.Clone(packet)
	c.outgoing = append(c.outgoing, queuedPacket{
		data:          buf,
		protocolBytes: protocolBytes,
		payloadBytes:  payloadBytes,
	})
}

func (c *Connection) PendingPackets() [][]byte {
	out := make([][]byte, len(c.outgoing))
	for i := range c.outgoing {
		out[i] = bytes.Clone(c.outgoing[i].data)
	}
	return out
}

func (c *Connection) PopOutgoing() []byte {
	if len(c.outgoing) == 0 {
		return nil
	}
	packet := c.outgoing[0].data
	c.outgoing = c.outgoing[1:]
	return packet
}

func (c *Connection) AppendIncoming(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	c.incoming = append(c.incoming, bytes.Clone(chunk))
}

func (c *Connection) prependIncoming(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	c.incoming = append([][]byte{bytes.Clone(chunk)}, c.incoming...)
}

func (c *Connection) IncomingChunks() [][]byte {
	out := make([][]byte, len(c.incoming))
	copy(out, c.incoming)
	return out
}

func (c *Connection) IncomingBytes() int {
	total := 0
	for _, chunk := range c.incoming {
		total += len(chunk)
	}
	return total
}

func (c *Connection) DrainIncoming() []byte {
	if len(c.incoming) == 0 {
		return nil
	}
	total := 0
	for _, chunk := range c.incoming {
		total += len(chunk)
	}
	buf := make([]byte, 0, total)
	for _, chunk := range c.incoming {
		buf = append(buf, chunk...)
	}
	c.incoming = c.incoming[:0]
	return buf
}

func (c *Connection) ConsumeIncoming(limit int) []byte {
	if limit <= 0 {
		return nil
	}
	raw := c.DrainIncoming()
	if len(raw) == 0 {
		return nil
	}
	if len(raw) <= limit {
		return raw
	}
	chunk := bytes.Clone(raw[:limit])
	c.prependIncoming(raw[limit:])
	return chunk
}

func (c *Connection) ReadFrames() ([]protocol.PacketHeader, [][]byte, error) {
	return c.ReadFramesWithCombiner(nil)
}

func (c *Connection) ReadFramesWithCombiner(combiner *protocol.PacketCombiner) ([]protocol.PacketHeader, [][]byte, error) {
	raw := c.DrainIncoming()
	if len(raw) == 0 {
		return nil, nil, nil
	}
	reader := bytes.NewReader(raw)
	headers := make([]protocol.PacketHeader, 0)
	bodies := make([][]byte, 0)
	if reader.Len() < protocol.PacketHeaderSize {
		// A partial eD2k header must survive until the next read. This is
		// especially important for accepted sockets where the inbound probe
		// replays the first five header bytes in their own chunk.
		c.prependIncoming(raw)
		return nil, nil, nil
	}
	for reader.Len() >= protocol.PacketHeaderSize {
		frameStart := len(raw) - reader.Len()
		var header protocol.PacketHeader
		if err := header.Get(reader); err != nil {
			logx.Debug("read frame header error", "raw_len", len(raw), "err", err)
			return headers, bodies, err
		}
		bodySize := int(header.SizePacket())
		serviceSize := bodySize
		if combiner != nil {
			serviceSize = combiner.ServiceSize(header)
		}
		if bodySize < 0 || serviceSize < 0 || serviceSize > bodySize {
			logx.Debug("read frame invalid header", "header", header.String(), "body_size", bodySize, "service_size", serviceSize, "remaining", reader.Len())
			return headers, bodies, io.ErrUnexpectedEOF
		}
		if reader.Len() < serviceSize {
			logx.Debug("read frame incomplete", "header", header.String(), "body_size", bodySize, "service_size", serviceSize, "remaining", reader.Len(), "raw_len", len(raw))
			c.prependIncoming(raw[frameStart:])
			break
		}
		body, err := protocol.ReadBytes(reader, serviceSize)
		if err != nil {
			logx.Debug("read frame body error", "header", header.String(), "body_size", bodySize, "service_size", serviceSize, "remaining", reader.Len(), "err", err)
			return headers, bodies, err
		}
		headers = append(headers, header)
		bodies = append(bodies, body)
		if bodySize > serviceSize {
			c.prependIncoming(raw[len(raw)-reader.Len():])
			return headers, bodies, nil
		}
	}
	return headers, bodies, nil
}

func (c *Connection) DecodeFrames(combiner *protocol.PacketCombiner) ([]protocol.PacketHeader, []protocol.Serializable, error) {
	if combiner == nil {
		return nil, nil, nil
	}
	headers, bodies, err := c.ReadFrames()
	if err != nil {
		return nil, nil, err
	}
	packets := make([]protocol.Serializable, 0, len(headers))
	for i, header := range headers {
		packet, err := combiner.Unpack(header, bodies[i])
		if err != nil {
			return headers[:i], packets, err
		}
		packets = append(packets, packet)
	}
	return headers, packets, nil
}

func (c *Connection) FlushOutgoing() error {
	if c.socket == nil {
		return nil
	}
	if len(c.outgoing) == 0 {
		return nil
	}
	packet := &c.outgoing[0]
	_ = c.socket.SetWriteDeadline(CurrentTimeToDeadline(connectionIODeadlineNS))
	n, err := c.socket.Write(packet.data)
	if err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return nil
		}
		c.Close(IOException)
		return err
	}
	if n >= len(packet.data) {
		c.stat.SendBytes(packet.protocolBytes, packet.payloadBytes)
		c.outgoing = c.outgoing[1:]
		return nil
	}
	packet.data = bytes.Clone(packet.data[n:])
	return nil
}
