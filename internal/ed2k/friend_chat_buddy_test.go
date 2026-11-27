package goed2k

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	clientproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/client"
	kadproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/kad"
)

// #7: KAD buddy packets now carry the sender TCP port and round-trip.
func TestKadBuddyPacketRoundTrip(t *testing.T) {
	var combiner kadproto.PacketCombiner
	for _, original := range []any{
		kadproto.FindBuddyReq{TCPPort: 4662},
		kadproto.FindBuddyRes{TCPPort: 4672},
		kadproto.CallbackReq{TCPPort: 4661},
	} {
		wire, err := combiner.Pack(original)
		if err != nil {
			t.Fatalf("pack %T: %v", original, err)
		}
		_, decoded, err := combiner.Unpack(wire)
		if err != nil {
			t.Fatalf("unpack %T: %v", original, err)
		}
		msg := decoded
		switch want := original.(type) {
		case kadproto.FindBuddyReq:
			if got := msg.(*kadproto.FindBuddyReq).TCPPort; got != want.TCPPort {
				t.Fatalf("buddy req port=%d want %d", got, want.TCPPort)
			}
		case kadproto.FindBuddyRes:
			if got := msg.(*kadproto.FindBuddyRes).TCPPort; got != want.TCPPort {
				t.Fatalf("buddy res port=%d want %d", got, want.TCPPort)
			}
		case kadproto.CallbackReq:
			if got := msg.(*kadproto.CallbackReq).TCPPort; got != want.TCPPort {
				t.Fatalf("callback port=%d want %d", got, want.TCPPort)
			}
		}
	}
}

// #6: friend list persists across save/load and chat packets round-trip.
func TestFriendListAndChat(t *testing.T) {
	hash := protocol.MustHashFromString("26DFB3060428ACECE9EC8864DE7126AE")
	fl := NewFriendList()
	if !fl.Add(Friend{UserHash: hash, Name: "alice"}) {
		t.Fatal("AddFriend failed")
	}
	if !fl.IsFriend(hash) {
		t.Fatal("friend not found")
	}
	path := filepath.Join(t.TempDir(), "friends.json")
	if err := fl.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded := NewFriendList()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.IsFriend(hash) {
		t.Fatal("friend not restored")
	}
	if !loaded.Remove(hash) || loaded.IsFriend(hash) {
		t.Fatal("RemoveFriend failed")
	}

	msg := clientproto.Message{Text: "hello eMule"}
	var buf bytes.Buffer
	if err := msg.Put(&buf); err != nil {
		t.Fatalf("put message: %v", err)
	}
	var got clientproto.Message
	if err := got.Get(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("get message: %v", err)
	}
	if got.Text != msg.Text {
		t.Fatalf("chat text=%q want %q", got.Text, msg.Text)
	}
}
