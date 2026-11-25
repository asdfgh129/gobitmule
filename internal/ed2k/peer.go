package goed2k

import "github.com/asdfgh129/gobitmule/internal/ed2k/protocol"

type Peer struct {
	LastConnected  int64
	NextConnection int64
	// DeadUntil quarantines an endpoint that accepts TCP but never answers our
	// Hello. Public KAD sources go stale in place: the same dead IP:port keeps
	// being published, and the generic FailCount backoff (20s, 30s, ...) wasted
	// a connection slot every cycle for ten minutes. A failed handshake sets a
	// short escalating quarantine here instead; a real HelloAnswer clears it.
	DeadUntil   int64
	DeadStrikes int
	FailCount   int
	Connectable bool
	SourceFlag  int
	Connection  any
	Endpoint    protocol.Endpoint
	// UserHash is the target peer's eD2k user hash used to arm outgoing
	// eMule basic obfuscation before Connect. Invalid means unknown, in
	// which case the connection stays in plaintext.
	UserHash protocol.Hash
}

func NewPeer(ep protocol.Endpoint) Peer {
	return Peer{Endpoint: ep}
}

func NewPeerWithSource(ep protocol.Endpoint, conn bool, sourceFlag int) Peer {
	return Peer{Endpoint: ep, Connectable: conn, SourceFlag: sourceFlag}
}

// NewPeerWithSourceAndHash builds a source candidate that can automatically
// negotiate outgoing obfuscation. A zero/invalid hash keeps the old behaviour
// (plaintext unless armed explicitly on the Connection).
func NewPeerWithSourceAndHash(ep protocol.Endpoint, conn bool, sourceFlag int, userHash protocol.Hash) Peer {
	return Peer{Endpoint: ep, Connectable: conn, SourceFlag: sourceFlag, UserHash: userHash}
}

// SetUserHash records a learned target hash. An invalid hash clears it.
func (p *Peer) SetUserHash(userHash protocol.Hash) {
	p.UserHash = userHash
}

func (p Peer) Compare(other Peer) int {
	return p.Endpoint.Compare(other.Endpoint)
}

func (p Peer) Equal(other Peer) bool {
	return p.Endpoint.Equal(other.Endpoint)
}
