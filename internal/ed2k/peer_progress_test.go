package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// A client that holds the finished file has no part bitmap to send, so its
// FileStatusAnswer carries a bare uint16 0. Reading that as "0 of the parts"
// made every source that was actively uploading report 0% — while the same peer
// was streaming SendingParts to us. eMule treats the empty bitmap as "complete";
// "I do not have this file" is a separate NoFileStatus packet.
func TestGetInfoTreatsEmptyBitmapAsCompleted(t *testing.T) {
	p := &PeerConnection{remoteStatusAnswered: true}
	// remotePieces stays zero-length, exactly as a decoded `00 00` bitmap.

	info := p.GetInfo()
	if !info.ProgressKnown {
		t.Fatal("an answered FileStatusRequest must be a known progress")
	}
	if info.Progress != 1 {
		t.Fatalf("progress = %v, want 1 (completed file)", info.Progress)
	}
}

func TestGetInfoReportsPartialBitmap(t *testing.T) {
	const pieces = 8
	p := &PeerConnection{remoteStatusAnswered: true}
	p.remotePieces = protocol.NewBitField(pieces)
	for _, i := range []int{0, 3, 4, 7} {
		p.remotePieces.SetBit(i)
	}

	info := p.GetInfo()
	if !info.ProgressKnown {
		t.Fatal("bitmap answer must be known progress")
	}
	if want := 0.5; info.Progress != want {
		t.Fatalf("progress = %v, want %v", info.Progress, want)
	}
}

// Before any answer the value is unknown, and the console must draw "—" rather
// than claim the peer has nothing.
func TestGetInfoLeavesProgressUnknownBeforeAnswer(t *testing.T) {
	p := &PeerConnection{}
	info := p.GetInfo()
	if info.ProgressKnown {
		t.Fatal("progress must stay unknown until the peer answers")
	}
	if info.Progress != 0 {
		t.Fatalf("progress = %v, want the zero value", info.Progress)
	}
}
