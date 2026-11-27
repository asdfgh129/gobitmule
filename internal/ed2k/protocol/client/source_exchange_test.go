package client

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

func mustHash(t *testing.T, raw string) protocol.Hash {
	t.Helper()
	hash, err := protocol.HashFromString(raw)
	if err != nil {
		t.Fatalf("hash %q: %v", raw, err)
	}
	return hash
}

// RequestSources2 is version + reserved + hash. Pinning the bytes keeps a
// refactor from quietly changing a layout that peers parse without a length
// prefix per field.
func TestRequestSources2WireLayout(t *testing.T) {
	packet := RequestSources2{
		Version: SourceExchangeVersion1,
		Hash:    mustHash(t, "E75AD307F1EC36B45DD100AB92A2F8C0"),
	}
	var buf bytes.Buffer
	if err := packet.Put(&buf); err != nil {
		t.Fatalf("put: %v", err)
	}
	want := "01" + "0000" + "e75ad307f1ec36b45dd100ab92a2f8c0"
	if got := hex.EncodeToString(buf.Bytes()); got != want {
		t.Fatalf("wire bytes %s, want %s", got, want)
	}
	if buf.Len() != packet.BytesCount() {
		t.Fatalf("BytesCount %d, wrote %d", packet.BytesCount(), buf.Len())
	}
}

func TestRequestSources2RoundTrip(t *testing.T) {
	packet := RequestSources2{
		Version: SourceExchangeVersion1,
		Hash:    mustHash(t, "11223344556677889900AABBCCDDEEFF"),
	}
	var buf bytes.Buffer
	if err := packet.Put(&buf); err != nil {
		t.Fatalf("put: %v", err)
	}
	var decoded RequestSources2
	if err := decoded.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get: %v", err)
	}
	if decoded.Version != packet.Version || !decoded.Hash.Equal(packet.Hash) {
		t.Fatalf("round trip mismatch: %+v vs %+v", decoded, packet)
	}
}

// A v1 answer carries 12-byte entries: hybrid ID, TCP port, source IP, aux port.
func TestAnswerSources2Version1Layout(t *testing.T) {
	packet := AnswerSources2{
		Version: 1,
		Hash:    mustHash(t, "E75AD307F1EC36B45DD100AB92A2F8C0"),
		Sources: []SourceExchangeSource{{
			UserID:  0x01020304,
			TCPPort: 4662,
			ServerIP:   0x3F5E7D8C,
			ServerPort: 4725,
		}},
	}
	var buf bytes.Buffer
	if err := packet.Put(&buf); err != nil {
		t.Fatalf("put: %v", err)
	}
	want := "01" +
		"e75ad307f1ec36b45dd100ab92a2f8c0" +
		"0100" +
		"04030201" + "3612" + "8c7d5e3f" + "7512"
	if got := hex.EncodeToString(buf.Bytes()); got != want {
		t.Fatalf("wire bytes %s\nwant       %s", got, want)
	}
	if buf.Len() != packet.BytesCount() {
		t.Fatalf("BytesCount %d, wrote %d", packet.BytesCount(), buf.Len())
	}
}

func TestAnswerSources2RoundTripAllVersions(t *testing.T) {
	hash := mustHash(t, "00112233445566778899AABBCCDDEEFF")
	userHash := mustHash(t, "FFEEDDCCBBAA99887766554433221100")
	for _, version := range []uint8{1, 2, 4} {
		packet := AnswerSources2{
			Version: version,
			Hash:    hash,
			Sources: []SourceExchangeSource{
				{UserID: 7, TCPPort: 4662, ServerIP: 0x01020304, ServerPort: 0, UserHash: userHash, CryptOptions: 3},
				{UserID: 9, TCPPort: 4661, ServerIP: 0x05060708, ServerPort: 12, UserHash: userHash, CryptOptions: 1},
			},
		}
		var buf bytes.Buffer
		if err := packet.Put(&buf); err != nil {
			t.Fatalf("v%d put: %v", version, err)
		}
		var decoded AnswerSources2
		if err := decoded.Get(bytes.NewReader(buf.Bytes())); err != nil {
			t.Fatalf("v%d get: %v", version, err)
		}
		if decoded.Version != version || !decoded.Hash.Equal(hash) {
			t.Fatalf("v%d header mismatch: %+v", version, decoded)
		}
		if len(decoded.Sources) != 2 {
			t.Fatalf("v%d decoded %d sources", version, len(decoded.Sources))
		}
		first := decoded.Sources[0]
		if first.UserID != 7 || first.TCPPort != 4662 || first.ServerIP != 0x01020304 {
			t.Fatalf("v%d first entry %+v", version, first)
		}
		if version >= 2 && !first.UserHash.Equal(userHash) {
			t.Fatalf("v%d lost the user hash", version)
		}
		if version >= 4 && first.CryptOptions != 3 {
			t.Fatalf("v%d crypt options %d", version, first.CryptOptions)
		}
	}
}

func TestAnswerSources2RejectsAbsurdCount(t *testing.T) {
	raw := []byte{1}
	raw = append(raw, mustHash(t, "E75AD307F1EC36B45DD100AB92A2F8C0").Bytes()...)
	raw = append(raw, 0xFF, 0xFF) // 65535 entries, more than any part file can use
	var decoded AnswerSources2
	if err := decoded.Get(bytes.NewReader(raw)); err == nil {
		t.Fatal("a 65535-entry answer must be rejected, not allocated")
	}
}

func TestAnswerSources2TruncatedEntry(t *testing.T) {
	raw := []byte{1}
	raw = append(raw, mustHash(t, "E75AD307F1EC36B45DD100AB92A2F8C0").Bytes()...)
	raw = append(raw, 0x01, 0x00) // claims one entry
	raw = append(raw, 0x01, 0x02) // two bytes of it
	var decoded AnswerSources2
	if err := decoded.Get(bytes.NewReader(raw)); err == nil {
		t.Fatal("a truncated entry must fail to parse")
	}
}

func TestHybridUserIDUsesTrailingFourBytes(t *testing.T) {
	hash := mustHash(t, "00000000000000000000000001020304")
	if got := HybridUserID(hash); got != 0x01020304 {
		t.Fatalf("hybrid id %#x, want 0x01020304", got)
	}
	if got := HybridUserID(protocol.Invalid); got != 0 {
		t.Fatalf("invalid hash hybrid id %#x, want 0", got)
	}
}
