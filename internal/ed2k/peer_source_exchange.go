package goed2k

import (
	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// Source exchange (SX2, opcodes 0x83/0x84) is how eMule clients hand each other
// source lists for the file they are both transferring. Without it a download
// only ever sees the sources Kad or the server returned, while a real eMule
// grows its list geometrically: every connected peer contributes the peers it
// already knows.
//
// The advertised capability lives in CT_EMULE_MISCOPTIONS1 bits 12-15
// (SourceExchange1Ver). A peer that sees zero never sends us a request and
// never answers ours, which is why nothing happened before.

// sourceExchangeAnswerLimit bounds one answer. eMule sends at most 200 sources
// per file; more than that is a waste of a frame.
const sourceExchangeAnswerLimit = 200

// maybeRequestSources asks a peer for its sources for our shared file. It is
// called once per connection, after the peer confirmed it has the file: asking
// earlier wastes a packet on peers that will drop us anyway.
func (p *PeerConnection) maybeRequestSources() {
	if p.sourceExchangeAsked || p.transfer == nil || p.session == nil {
		return
	}
	if p.transfer.IsFinished() {
		return
	}
	// The peer advertises SX support in its Hello; without it the request is
	// ignored, and worse, a peer that does not speak SX2 may answer with the
	// deprecated 0x81/0x82 pair that we do not parse.
	// aMule gates SX2 on MiscOptions2's SourceExchange2 flag; eMule mods also
	// advertise it in MiscOptions1. Accept either, and use the richer version
	// the peer is capable of.
	version := p.remoteSourceExchangeVersion()
	if version <= 0 {
		return
	}
	hash := p.transfer.GetHash()
	packet := &clientproto.RequestSources2{
		Version: uint8(version),
		Hash:    hash,
	}
	raw, err := p.combiner.Pack("client.RequestSources2", packet)
	if err != nil {
		debugPeerf("peer %s -> RequestSources2 pack failed: %v", p.endpoint.String(), err)
		return
	}
	p.sourceExchangeAsked = true
	debugPeerf("peer %s -> RequestSources2 hash=%s sx2=%d hex=%X",
		p.endpoint.String(), hash.String(), version, raw)
	p.QueuePacket(raw)
}

// HandleRequestSources2 answers a peer's source request with the sources we
// know. An empty answer is not sent at all: eMule treats a zero-entry answer as
// noise, and there is nothing to learn from it.
func (p *PeerConnection) HandleRequestSources2(value *clientproto.RequestSources2) {
	if value == nil || p.session == nil {
		return
	}
	debugPeerf("peer %s <- RequestSources2 hash=%s version=%d",
		p.endpoint.String(), value.Hash.String(), value.Version)
	transfer := p.session.LookupTransfer(value.Hash)
	if transfer == nil {
		return
	}
	// Answer in the version the requester understands, never above ours.
	version := int(value.Version)
	if maxVersion := clientproto.SourceExchangeVersion; version > maxVersion {
		version = maxVersion
	}
	if version < 1 {
		version = 1
	}
	sources := transfer.sourceExchangeEntries(p.endpoint.IP(), version, sourceExchangeAnswerLimit)
	if len(sources) == 0 {
		debugPeerf("peer %s <- RequestSources2 hash=%s no sources to offer",
			p.endpoint.String(), value.Hash.String())
		return
	}
	answer := &clientproto.AnswerSources2{
		Version: uint8(version),
		Hash:    value.Hash,
		Sources: sources,
	}
	raw, err := p.combiner.Pack("client.AnswerSources2", answer)
	if err != nil {
		debugPeerf("peer %s -> AnswerSources2 pack failed: %v", p.endpoint.String(), err)
		return
	}
	debugPeerf("peer %s -> AnswerSources2 hash=%s version=%d count=%d bytes=%d",
		p.endpoint.String(), value.Hash.String(), version, len(sources), len(raw))
	p.QueuePacket(raw)
}

// HandleAnswerSources2 merges a peer's source list into the transfer policy.
// Entries that cannot be dialled (low ID, zero port) are dropped: adding them
// would only produce connect attempts that always fail.
func (p *PeerConnection) HandleAnswerSources2(value *clientproto.AnswerSources2) {
	if value == nil || p.session == nil {
		return
	}
	transfer := p.session.LookupTransfer(value.Hash)
	if transfer == nil && p.transfer != nil && p.transfer.GetHash().Equal(value.Hash) {
		transfer = p.transfer
	}
	if transfer == nil {
		debugPeerf("peer %s <- AnswerSources2 hash=%s dropped: no such transfer",
			p.endpoint.String(), value.Hash.String())
		return
	}
	added, dialable := 0, 0
	for _, entry := range value.Sources {
		// aMule writes the source's hybrid ID first; for a high-ID client that
		// is its network-order IP. Low-ID entries cannot be dialled directly.
		if entry.TCPPort == 0 || entry.UserID == 0 || IsLowID(int32(entry.UserID)) {
			continue
		}
		dialable++
		endpoint := protocol.NewEndpoint(int32(entry.UserID), int(entry.TCPPort))
		if transfer.policy.Get(endpoint) != nil {
			continue
		}
		hash := entry.UserHash
		if value.Version < 2 {
			hash = protocol.Invalid
		}
		if err := transfer.AddPeerWithHash(endpoint, int(PeerSourceExchange), hash); err == nil {
			added++
		}
	}
	debugPeerf("peer %s <- AnswerSources2 hash=%s version=%d entries=%d dialable=%d added=%d live=%d",
		p.endpoint.String(), value.Hash.String(), value.Version,
		len(value.Sources), dialable, added, transfer.policy.Size())
}

// sourceExchangeEntries renders our known sources for an SX answer.
func (t *Transfer) sourceExchangeEntries(excludeIP int32, version, limit int) []clientproto.SourceExchangeSource {
	entries := make([]clientproto.SourceExchangeSource, 0, limit)
	seen := make(map[protocol.Endpoint]struct{}, len(t.policy.peers))
	for i := range t.policy.peers {
		peer := &t.policy.peers[i]
		if peer.Endpoint.IP() == excludeIP {
			// Never hand a peer its own address back.
			continue
		}
		if !peer.Endpoint.Defined() || IsLowID(peer.Endpoint.IP()) {
			continue
		}
		if _, ok := seen[peer.Endpoint]; ok {
			continue
		}
		seen[peer.Endpoint] = struct{}{}
		entry := clientproto.SourceExchangeSource{
			TCPPort:    uint16(peer.Endpoint.Port()),
			ServerIP:   uint32(peer.Endpoint.IP()),
			ServerPort: uint16(peer.Endpoint.Port()),
		}
		// A high-ID source is dialled by its user ID / network-order IP. Peers
		// that do not know the user hash still get a valid ID from the socket.
		if !peer.UserHash.Equal(protocol.Invalid) {
			entry.UserID = clientproto.HybridUserID(peer.UserHash)
		} else {
			entry.UserID = uint32(peer.Endpoint.IP())
		}
		if version >= 2 && !peer.UserHash.Equal(protocol.Invalid) {
			entry.UserHash = peer.UserHash
		}
		entries = append(entries, entry)
		if len(entries) >= limit {
			break
		}
	}
	return entries
}

// remoteSourceExchangeVersion returns the highest SX version the peer
// advertised. aMule's MiscOptions2 bit 10 is the authoritative SX2 flag; the
// older MiscOptions1 nibble is still emitted by some mods.
func (p *PeerConnection) remoteSourceExchangeVersion() int {
	// eMule requests SOURCEEXCHANGE2_VERSION (4) unconditionally from any peer
	// whose CT_EMULE_MISCOPTIONS2 bit 10 says it speaks SX2. The Misc1 nibble is
	// the SX1 version; some mods leave it at 0 while still supporting SX2.
	supported := p.remotePeerInfo.Misc2.SupportSourceExt2() || p.remotePeerInfo.Misc1.SourceExchange1Ver > 0
	if !supported {
		return 0
	}
	return clientproto.SourceExchangeVersion
}
