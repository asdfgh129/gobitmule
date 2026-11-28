package goed2k

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

// Legacy source requests must be selectable even when OP_SERVERIDENT carried
// no capability bits: the interop probe forces 0x9A specifically to prove the
// real server still answers the eMule legacy datagram.
func TestForcedUDPSourcesOpcodeOnRealSocket(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer func() { _ = listener.Close() }()
	addr := listener.LocalAddr().(*net.UDPAddr)
	if addr.Port < 5 {
		t.Fatalf("ephemeral port too small: %d", addr.Port)
	}
	// The session derives the server UDP endpoint as TCP port + 4.
	serverAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: addr.Port - 4}
	session := NewSession(NewSettings())
	defer session.serverUDP.Close()
	session.serverConnection = NewServerConnection("forced-udp", serverAddr, session)
	if err := session.serverUDP.Start(0); err != nil {
		t.Fatalf("start server udp: %v", err)
	}
	hash, err := protocol.HashFromString("1565D4FAE4B5F20EC77429622BC46C90")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	cases := []struct {
		opcode byte
		want   []byte
	}{
		{serverproto.OpGlobGetSources, serverproto.UDPGlobGetSourcesLegacy(hash)},
		{serverproto.OpGlobGetSources2, serverproto.UDPGlobGetSources(hash, 663748608)},
	}
	for _, tc := range cases {
		session.SetForcedUDPSourcesOpcode(tc.opcode)
		if !session.SendUDPSourcesRequest(hash, 663748608) {
			t.Fatalf("send opcode 0x%02x failed", tc.opcode)
		}
		if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		buf := make([]byte, 128)
		n, _, err := listener.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("read opcode 0x%02x: %v", tc.opcode, err)
		}
		if !bytes.Equal(buf[:n], tc.want) {
			t.Fatalf("opcode 0x%02x wire = %x, want %x", tc.opcode, buf[:n], tc.want)
		}
	}
}
