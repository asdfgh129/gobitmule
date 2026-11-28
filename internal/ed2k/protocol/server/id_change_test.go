package server

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// eMule reads a 20-byte OP_IDCHANGE: <clientID><tcpFlags><auxPort>
// <serverReportedIP><obfuscationTCPPort>. The fourth field is what
// CemuleApp::GetPublicIP() returns and what ST_UDPKEYIP must match.
func TestIdChangeParsesReportedPublicIP(t *testing.T) {
	const (
		clientID = int32(0x01512345) // LOW_ID
		flags    = int32(0x0000011D)
		auxPort  = int32(4711)
		pubIP    = uint32(0x3980A118) // 57.128.161.24
		obfPort  = uint32(4712)
	)
	var buf bytes.Buffer
	_ = protocol.WriteInt32(&buf, clientID)
	_ = protocol.WriteInt32(&buf, flags)
	_ = protocol.WriteInt32(&buf, auxPort)
	_ = protocol.WriteUInt32(&buf, pubIP)
	_ = protocol.WriteUInt32(&buf, obfPort)
	raw := buf.Bytes()
	if len(raw) != 20 {
		t.Fatalf("raw login answer = %d bytes, want 20", len(raw))
	}

	var id IdChange
	if err := id.Get(bytes.NewReader(raw)); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if id.ClientID != clientID || id.TCPFlags != flags || id.AuxPort != auxPort {
		t.Fatalf("legacy fields = %d/%#x/%d", id.ClientID, id.TCPFlags, id.AuxPort)
	}
	if id.ReportedIP != pubIP {
		t.Fatalf("ReportedIP = %#x, want %#x", id.ReportedIP, pubIP)
	}
	if id.ObfuscationTCPPort != obfPort {
		t.Fatalf("ObfuscationTCPPort = %d, want %d", id.ObfuscationTCPPort, obfPort)
	}
}

// Legacy 12-byte servers, and bogus LOW_ID-looking reported IPs, must not
// invent a public address that later disables UDP obfuscation.
func TestIdChangeLegacyAndBogusReportedIP(t *testing.T) {
	var legacy bytes.Buffer
	_ = protocol.WriteInt32(&legacy, 0x12345678)
	_ = protocol.WriteInt32(&legacy, 0)
	_ = protocol.WriteInt32(&legacy, 4672)
	var id IdChange
	if err := id.Get(bytes.NewReader(legacy.Bytes())); err != nil {
		t.Fatalf("legacy Get: %v", err)
	}
	if id.ReportedIP != 0 || id.ObfuscationTCPPort != 0 {
		t.Fatalf("legacy answer invented address %#x / port %d", id.ReportedIP, id.ObfuscationTCPPort)
	}

	var bogus bytes.Buffer
	_ = protocol.WriteInt32(&bogus, 0x01512345)
	_ = protocol.WriteInt32(&bogus, 0)
	_ = protocol.WriteInt32(&bogus, 0)
	_ = protocol.WriteUInt32(&bogus, 0x00FFFFFF) // eMule: IsLowID => ignore
	_ = protocol.WriteUInt32(&bogus, 0)
	id = IdChange{}
	if err := id.Get(bytes.NewReader(bogus.Bytes())); err != nil {
		t.Fatalf("bogus Get: %v", err)
	}
	if id.ReportedIP != 0 {
		t.Fatalf("LOW_ID-looking reported IP accepted: %#x", id.ReportedIP)
	}
}

func TestIdChangePutRoundTrip(t *testing.T) {
	want := IdChange{
		ClientID:           0x3980A118,
		TCPFlags:           0x1D,
		AuxPort:            4711,
		ReportedIP:         0x3980A118,
		ObfuscationTCPPort: 4712,
	}
	var buf bytes.Buffer
	if err := want.Put(&buf); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if buf.Len() != want.BytesCount() {
		t.Fatalf("Put wrote %d bytes, BytesCount() = %d", buf.Len(), want.BytesCount())
	}
	var got IdChange
	if err := got.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}
