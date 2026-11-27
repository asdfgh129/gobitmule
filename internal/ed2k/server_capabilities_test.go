package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

// eMule persists ST_UDPFLAGS in server.met (server.cpp case ST_UDPFLAGS), so a
// server.met produced by eMule already tells us which datagram opcode a server
// can parse, before we ever log in to it.
func TestServerMetEntryCarriesUDPFlags(t *testing.T) {
	entry := serverproto.ServerMetEntry{
		Endpoint: mustEndpoint(t, "57.128.161.24", 6666),
		Tags: protocol.TagList{
			protocol.NewStringTag(0x01, "Cyberchrist"),
			protocol.NewUInt32Tag(serverproto.TagUDPFlags, 0x73b),
			protocol.NewUInt32Tag(serverproto.TagUDPKey, 0x11223344),
			protocol.NewUInt32Tag(serverproto.TagUDPKeyIP, 0x01020304),
		},
	}
	if got := entry.UDPFlags(); got != 0x73b {
		t.Fatalf("UDPFlags() = %#x, want 0x73b", got)
	}
	key, keyIP := entry.UDPKey()
	if key != 0x11223344 || keyIP != 0x01020304 {
		t.Fatalf("UDPKey() = (%#x, %#x)", key, keyIP)
	}
	if got := entry.EndpointKey(); got != "57.128.161.24:6666" {
		t.Fatalf("EndpointKey() = %q", got)
	}
}

// A met entry without the tag must report zero rather than panic.
func TestServerMetEntryWithoutUDPFlags(t *testing.T) {
	entry, err := serverproto.NewServerMetEntryFromHost("example.invalid", 4661, "x", "")
	if err != nil {
		t.Fatalf("build entry: %v", err)
	}
	if got := entry.UDPFlags(); got != 0 {
		t.Fatalf("UDPFlags() = %#x, want 0", got)
	}
	if entry.EndpointKey() != "" {
		t.Fatalf("EndpointKey() for a host-only entry = %q, want empty", entry.EndpointKey())
	}
}

// The opcode must follow the advertised capability bits exactly like eMule's
// CSearchResultsWnd::DoNewED2KSearch: 0x90 needs EXT_GETFILES + LARGEFILES,
// 0x92 needs EXT_GETFILES, everything else falls back to the legacy 0x98.
func TestGlobSearchOpcodeFollowsServerCapabilities(t *testing.T) {
	const (
		extGetFiles = serverproto.UDPFlagExtGetFiles
		largeFiles  = serverproto.UDPFlagLargeFiles
	)
	cases := []struct {
		name  string
		flags uint32
		want  byte
	}{
		{"cyberchrist 0x73b", 0x73b, serverproto.OpGlobSearchReq},
		{"no udp flags", 0x0, serverproto.OpGlobSearchReqV1},
		{"only ext getfiles", extGetFiles, serverproto.OpGlobSearchReqV2},
		{"getfiles without largefiles", extGetFiles | 0x20, serverproto.OpGlobSearchReqV2},
		{"largefiles without getfiles", largeFiles, serverproto.OpGlobSearchReqV1},
	}
	req := &serverproto.SearchRequest{Query: "debian"}
	for _, tc := range cases {
		datagram, err := serverproto.UDPGlobSearchDatagram(req, tc.flags)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(datagram) < 2 {
			t.Fatalf("%s: datagram too short", tc.name)
		}
		if datagram[1] != tc.want {
			t.Fatalf("%s: opcode = %#x, want %#x", tc.name, datagram[1], tc.want)
		}
	}
}

// Seeding the session cache from server.met is what lets the first search after
// startup choose the right opcode; both the display address and the numeric
// endpoint must be usable as keys.
func TestSeedServerCapabilitiesFromServerMet(t *testing.T) {
	s := &Session{}
	entries := []serverproto.ServerMetEntry{
		{
			Endpoint: mustEndpoint(t, "57.128.161.24", 6666),
			Tags: protocol.TagList{
				protocol.NewStringTag(0x01, "Cyberchrist"),
				protocol.NewUInt32Tag(serverproto.TagUDPFlags, 0x73b),
			},
		},
		{
			Endpoint: mustEndpoint(t, "10.0.0.1", 4661),
			Tags:     protocol.TagList{protocol.NewStringTag(0x01, "no-udp")},
		},
	}
	if seeded := s.SeedServerCapabilities(entries); seeded != 1 {
		t.Fatalf("SeedServerCapabilities = %d, want 1", seeded)
	}
	if got := s.serverUDPCapabilities["57.128.161.24:6666"]; got != 0x73b {
		t.Fatalf("cached flags = %#x, want 0x73b", got)
	}
	if _, ok := s.serverUDPCapabilities["10.0.0.1:4661"]; ok {
		t.Fatal("a server without ST_UDPFLAGS must not be cached")
	}
}

// Learn-once behaviour: an OP_SERVERIDENT updates the cache so the next request
// does not depend on the handshake that is still in flight.
func TestRememberServerCapabilitiesOverwrites(t *testing.T) {
	s := &Session{}
	s.RememberServerCapabilities("57.128.161.24:6666", 0x1)
	s.RememberServerCapabilities("57.128.161.24:6666", 0x73b)
	if got := s.serverUDPCapabilities["57.128.161.24:6666"]; got != 0x73b {
		t.Fatalf("cached flags = %#x, want 0x73b", got)
	}
}

func mustEndpoint(t *testing.T, ip string, port int) protocol.Endpoint {
	t.Helper()
	endpoint, err := protocol.EndpointFromString(ip, port)
	if err != nil {
		t.Fatalf("endpoint %s:%d: %v", ip, port, err)
	}
	return endpoint
}
