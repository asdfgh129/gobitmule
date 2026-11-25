package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
)

// A source poll that was just issued must own its next deadline. The old
// SecondTick "acceleration" pulled every future deadline back to "now", so a
// 30s KAD retry became a request on every 200ms tick (50+ searches in 10s).
func TestSecondTickKeepsFutureSourceDeadlines(t *testing.T) {
	session := NewSession(NewSettings())
	transfer, err := NewTransfer(session, AddTransferParams{
		Hash:       protocol.MustHashFromString("39F8BAB86C16143B3152807FBCA89B5E"),
		CreateTime: CurrentTime(),
		Size:       1024,
	})
	if err != nil {
		t.Fatalf("NewTransfer: %v", err)
	}

	now := CurrentTime()
	transfer.nextSourcesRequest = now + Minutes(1)
	transfer.nextDHTRequest = now + Minutes(1)

	transfer.SecondTick(nil, 200)

	if got := transfer.nextSourcesRequest; got < now+Seconds(50) {
		t.Fatalf("server source deadline was pulled forward: got now+%dms, want >= now+50s", got-now)
	}
	if got := transfer.nextDHTRequest; got < now+Seconds(50) {
		t.Fatalf("DHT source deadline was pulled forward: got now+%dms, want >= now+50s", got-now)
	}
}

// QueueRanking is queue progress, not a disconnect instruction. Dropping the
// peer here prevents a later AcceptUpload and wastes a willing uploader.
func TestQueueRankingKeepsPeerConnected(t *testing.T) {
	pc := &PeerConnection{}
	pc.HandleQueueRanking(&clientproto.QueueRanking{Rank: 42})
	if pc.remoteQueueRank != 42 {
		t.Fatalf("remote queue rank = %d, want 42", pc.remoteQueueRank)
	}
	if pc.IsDisconnecting() {
		t.Fatal("QueueRanking must not disconnect the peer")
	}
}
