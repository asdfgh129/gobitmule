package goed2k

import (
	"bytes"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

func sxTestSetup(t *testing.T, hash protocol.Hash) (*Session, *Transfer, *PeerConnection) {
	t.Helper()
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       hash,
		CreateTime: CurrentTime(),
		Size:       40 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}
	session.mu.Lock()
	session.transfers[hash] = transfer
	session.mu.Unlock()
	pc := NewPeerConnection(session, protocol.NewEndpoint(0x01020304, 4662), transfer, nil)
	pc.endpoint = protocol.NewEndpoint(0x01020304, 4662)
	return session, transfer, pc
}

// eMule only sends SX2 traffic to peers advertising MiscOptions2 bit 10. The
// request must carry SOURCEEXCHANGE2_VERSION (4), reserved=0 and the file hash.
func TestMaybeRequestSourcesUsesSX2(t *testing.T) {
	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	_, _, pc := sxTestSetup(t, hash)
	pc.maybeRequestSources()
	if len(pc.outgoing) != 0 {
		t.Fatalf("a peer without SX2 support must not get a request")
	}
	pc.remotePeerInfo.Misc2.Value = 1 << 10
	pc.maybeRequestSources()
	if len(pc.outgoing) != 1 {
		t.Fatalf("queued %d packets, want 1", len(pc.outgoing))
	}
	raw := pc.outgoing[0].data
	if len(raw) < 8 || raw[0] != 0xC5 || raw[5] != 0x83 {
		t.Fatalf("not an eMule RequestSources2 frame: %X", raw)
	}
	want := []byte{0x04, 0x00, 0x00}
	want = append(want, hash.Bytes()...)
	if !bytes.Equal(raw[6:], want) {
		t.Fatalf("payload %X, want %X", raw[6:], want)
	}
	pc.maybeRequestSources()
	if len(pc.outgoing) != 1 {
		t.Fatalf("a second tick repeated the request: %d packets", len(pc.outgoing))
	}
}

// A v4 answer is merged into the policy: high-ID entries become dialable
// sources, low-ID entries are dropped, known hashes are carried through.
func TestHandleAnswerSources2MergesDialableSources(t *testing.T) {
	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	userHash := protocol.MustHashFromString("00112233445566778899AABBCCDDEEFF")
	_, transfer, pc := sxTestSetup(t, hash)
	answer := &clientproto.AnswerSources2{
		Version: 4,
		Hash:    hash,
		Sources: []clientproto.SourceExchangeSource{
			{UserID: 0x3F5E7D8C, TCPPort: 4662, ServerIP: 0x01020304, ServerPort: 4712, UserHash: userHash, CryptOptions: 1},
			{UserID: 0x00ABCDEF, TCPPort: 4662, ServerIP: 0x01020304, ServerPort: 4712}, // low ID
			{UserID: 0x3F5E7D8C, TCPPort: 4662, ServerIP: 0x01020304, ServerPort: 4712}, // duplicate
		},
	}
	pc.HandleAnswerSources2(answer)
	if transfer.policy.Size() != 1 {
		t.Fatalf("policy has %d sources, want 1", transfer.policy.Size())
	}
	peer := transfer.policy.peers[0]
	if peer.Endpoint.IP() != int32(0x3F5E7D8C) || peer.Endpoint.Port() != 4662 {
		t.Fatalf("source endpoint %v", peer.Endpoint.String())
	}
	if peer.SourceFlag&int(PeerSourceExchange) == 0 {
		t.Fatalf("source flag %d lacks PeerSourceExchange", peer.SourceFlag)
	}
	if !peer.UserHash.Equal(userHash) {
		t.Fatalf("source hash %s, want %s", peer.UserHash.String(), userHash.String())
	}
}

func TestHandleAnswerSources2IgnoresAnotherFile(t *testing.T) {
	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	other := protocol.MustHashFromString("FFEEDDCCBBAA99887766554433221100")
	_, transfer, pc := sxTestSetup(t, hash)
	pc.HandleAnswerSources2(&clientproto.AnswerSources2{
		Version: 4,
		Hash:    other,
		Sources: []clientproto.SourceExchangeSource{{UserID: 0x3F5E7D8C, TCPPort: 4662}},
	})
	if transfer.policy.Size() != 0 {
		t.Fatalf("a source for another file was merged: %d", transfer.policy.Size())
	}
}

// An answer must contain only dialable high-ID sources, and must not hand the
// requester its own address back.
// aMule's CPartFile::CreateSrcInfoPacket returns NULL when m_SrcList is empty,
// and eMule likewise sends no answer when it has no sources to offer. This is
// the reason real eMule/aMule peers we download FROM never answer our 0x83:
// they are complete sources with no srcList of their own, not downloaders of
// the same file. Keep our answer path symmetric: no sources -> no packet.
func TestHandleRequestSources2WithoutSourcesSendsNothing(t *testing.T) {
	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	_, _, pc := sxTestSetup(t, hash)
	pc.HandleRequestSources2(&clientproto.RequestSources2{Version: 4, Hash: hash})
	if len(pc.outgoing) != 0 {
		t.Fatalf("a peer with no sources must not send an empty answer: %d packets", len(pc.outgoing))
	}
}

func TestHandleRequestSources2OffersKnownSources(t *testing.T) {
	hash := protocol.MustHashFromString("E75AD307F1EC36B45DD100AB92A2F8C0")
	_, transfer, pc := sxTestSetup(t, hash)
	if err := transfer.AddPeer(protocol.NewEndpoint(0x01020304, 4662), int(PeerDHT)); err != nil {
		t.Fatal(err)
	}
	if err := transfer.AddPeer(protocol.NewEndpoint(0x05060708, 4662), int(PeerServer)); err != nil {
		t.Fatal(err)
	}
	pc.HandleRequestSources2(&clientproto.RequestSources2{Version: 4, Hash: hash})
	if len(pc.outgoing) != 1 {
		t.Fatalf("queued %d packets, want 1 answer", len(pc.outgoing))
	}
	raw := pc.outgoing[0].data
	if raw[5] != 0x84 {
		t.Fatalf("opcode %#x, want 0x84", raw[5])
	}
	var answer clientproto.AnswerSources2
	// raw = 5-byte header prefix + opcode + body; the packet body starts at 6.
	if err := answer.Get(bytes.NewReader(raw[6:])); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if answer.Version != 4 || !answer.Hash.Equal(hash) {
		t.Fatalf("answer header version=%d hash=%s", answer.Version, answer.Hash.String())
	}
	if len(answer.Sources) != 1 {
		t.Fatalf("answer has %d sources, want 1 (self excluded)", len(answer.Sources))
	}
	if answer.Sources[0].ServerIP != 0x05060708 || answer.Sources[0].TCPPort != 4662 {
		t.Fatalf("answer source %+v", answer.Sources[0])
	}
}
