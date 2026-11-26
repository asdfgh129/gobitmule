package goed2k

import (
	"strings"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

const (
	PeerIncoming byte = 0x1
	PeerServer   byte = 0x2
	PeerDHT      byte = 0x4
	PeerResume   byte = 0x8
)

type PeerInfo struct {
	DownloadSpeed        int
	PayloadDownloadSpeed int
	UploadSpeed          int
	PayloadUploadSpeed   int
	RemotePieces         protocol.BitField
	FailCount            int
	Endpoint             protocol.Endpoint
	ModName              string
	Version              int
	ModVersion           int
	StrModVersion        string
	SourceFlag           int
	// State/queue/progress make the peer list useful in the Web console: eMule
	// shows the same columns (state, queue position, remote progress) in its
	// download client list.
	State           string
	QueueRank       int
	RemoteQueueRank int
	Progress        float64
	// UserHash is the remote peer's eD2k user hash learned from a cleartext
	// Hello/HelloAnswer. Invalid means not learned yet. It is what makes
	// ED-01 "both sides see each other" observable without pcap parsing.
	UserHash protocol.Hash
	// RetryIn and DeadStrikes explain a source that is known but not connected.
	// eMule's client list shows the same information as 等待中 / 已失效 instead of
	// hiding every source that is not downloading right now.
	RetryIn     int64
	DeadStrikes int
}

// sourceStateRank orders sources the way an eMule client list reads: the ones
// moving bytes first, then queue/handshake, then idle candidates, then dead.
func sourceStateRank(state string) int {
	switch state {
	case "downloading":
		return 0
	case "requesting":
		return 1
	case "connected":
		return 2
	case "queued":
		return 3
	case "handshaking":
		return 4
	case "connecting":
		return 5
	case "waiting":
		return 6
	case "unconnectable":
		return 7
	case "disconnecting":
		return 8
	case "dead":
		return 9
	}
	return 10
}

// idleSourceState names the reason a known source has no live connection yet.
func idleSourceState(now int64, p Peer) (string, int64) {
	switch {
	case p.DeadUntil > now:
		return "dead", p.DeadUntil - now
	case !p.Connectable:
		// LowID source: it cannot accept our call, we have to wait for it.
		return "unconnectable", 0
	case p.NextConnection > now:
		return "waiting", p.NextConnection - now
	}
	return "connecting", 0
}

func PeerSourceLabels(sourceFlag int) []string {
	labels := make([]string, 0, 4)
	if (sourceFlag & int(PeerServer)) != 0 {
		labels = append(labels, "server")
	}
	if (sourceFlag & int(PeerDHT)) != 0 {
		labels = append(labels, "kad")
	}
	if (sourceFlag & int(PeerResume)) != 0 {
		labels = append(labels, "resume")
	}
	if (sourceFlag & int(PeerIncoming)) != 0 {
		labels = append(labels, "incoming")
	}
	if len(labels) == 0 {
		labels = append(labels, "unknown")
	}
	return labels
}

func (p PeerInfo) SourceLabels() []string {
	return PeerSourceLabels(p.SourceFlag)
}

func (p PeerInfo) SourceString() string {
	return strings.Join(p.SourceLabels(), "|")
}

func (p PeerInfo) HasSource(source byte) bool {
	return (p.SourceFlag & int(source)) != 0
}
