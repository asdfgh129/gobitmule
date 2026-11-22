package goed2k

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asdfgh129/ud/internal/ed2k/protocol"
)

func TestUserHashAndSecureIdentPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client-state.json")
	wantHash := protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF")

	first := NewClient(NewSettings())
	first.SetStatePath(path)
	if err := first.SetUserHash(wantHash); err != nil {
		t.Fatalf("SetUserHash: %v", err)
	}
	first.SetSecureIdent(true)
	if err := first.SaveState(""); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	first.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var persisted ClientState
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if !persisted.UserHash.Equal(wantHash) {
		t.Fatalf("persisted user_hash = %s, want %s", persisted.UserHash, wantHash)
	}
	if !persisted.SecureIdent {
		t.Fatal("persisted secure_ident = false, want true")
	}

	second := NewClient(NewSettings())
	second.SetStatePath(path)
	if err := second.LoadState(""); err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	defer second.Close()
	if got := second.UserHash(); !got.Equal(wantHash) {
		t.Fatalf("restarted UserHash = %s, want %s", got, wantHash)
	}
	if !second.SecureIdent() {
		t.Fatal("restarted SecureIdent = false, want true")
	}
}

func TestSetUserHashRejectsInvalidHash(t *testing.T) {
	c := NewClient(NewSettings())
	defer c.Close()
	if err := c.SetUserHash(protocol.Invalid); err == nil {
		t.Fatal("SetUserHash accepted invalid hash")
	}
}
