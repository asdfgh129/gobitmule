package goed2k

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/asdfgh129/ud/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/ud/internal/ed2k/protocol/server"
)

// TestLowIDSimulatedServerHandshake covers ED-04's "Low ID (NAT) simulation"
// without depending on a public eD2k server: a local fake server sends an
// IdChange frame with a low client ID and the client must classify it as
// LOW_ID while keeping the session alive.
func TestLowIDSimulatedServerHandshake(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen fake server: %v", err)
	}
	defer listener.Close()

	lowID := int32(123456)
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := readServerFrame(conn); err != nil {
			return
		}
		combiner := serverproto.NewPacketCombiner()
		raw, err := combiner.Pack("server.IdChange", &serverproto.IdChange{
			ClientID: lowID,
			TCPFlags: 0,
			AuxPort:  0,
		})
		if err != nil {
			return
		}
		if _, err := conn.Write(raw); err != nil {
			return
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()

	settings := NewSettings()
	settings.ListenPort = 0
	settings.EnableDHT = false
	client := NewClient(settings)
	defer client.Close()
	if err := client.Start(); err != nil {
		t.Fatalf("client Start: %v", err)
	}
	if err := client.Connect(listener.Addr().String()); err != nil {
		t.Fatalf("client Connect: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range client.ServerStatuses() {
			if !status.HandshakeCompleted {
				continue
			}
			if status.ClientID != lowID {
				t.Fatalf("server client ID = %d, want %d", status.ClientID, lowID)
			}
			if !IsLowID(status.ClientID) {
				t.Fatalf("client ID %d was not classified as low ID", status.ClientID)
			}
			if class := status.IDClass(); class != "LOW_ID" {
				t.Fatalf("IDClass = %s, want LOW_ID", class)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake server handshake did not complete")
}

func readServerFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, protocol.PacketHeaderSize)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	var parsed protocol.PacketHeader
	if err := parsed.Get(bytes.NewReader(header)); err != nil {
		return nil, err
	}
	bodyLen := int(parsed.SizePacket())
	if bodyLen < 0 {
		return nil, io.ErrUnexpectedEOF
	}
	frame := make([]byte, protocol.PacketHeaderSize+bodyLen)
	copy(frame, header)
	if _, err := io.ReadFull(conn, frame[protocol.PacketHeaderSize:]); err != nil {
		return nil, err
	}
	return frame, nil
}
