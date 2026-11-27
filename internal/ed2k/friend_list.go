package goed2k

// #6: eMule-style friend list. Friends are identified by user hash and may
// carry a saved name and Kad ID. The list is persisted as JSON by the session
// state store, so reconnects keep friend slots and chat identities.

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

type Friend struct {
	UserHash protocol.Hash `json:"userHash"`
	Name     string        `json:"name"`
	KadID    string        `json:"kadID,omitempty"`
}

type FriendList struct {
	mu      sync.RWMutex
	friends map[string]*Friend
}

func NewFriendList() *FriendList {
	return &FriendList{friends: make(map[string]*Friend)}
}

func (f *FriendList) Add(friend Friend) bool {
	if friend.UserHash.Equal(protocol.Invalid) {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.friends[friend.UserHash.String()]; exists {
		return false
	}
	copyFriend := friend
	f.friends[friend.UserHash.String()] = &copyFriend
	return true
}

func (f *FriendList) Remove(hash protocol.Hash) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.friends[hash.String()]; !exists {
		return false
	}
	delete(f.friends, hash.String())
	return true
}

func (f *FriendList) IsFriend(hash protocol.Hash) bool {
	if hash.Equal(protocol.Invalid) {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.friends[hash.String()]
	return ok
}

func (f *FriendList) List() []Friend {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Friend, 0, len(f.friends))
	for _, friend := range f.friends {
		out = append(out, *friend)
	}
	return out
}

func (f *FriendList) Marshal() ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return json.Marshal(f.List())
}

func (f *FriendList) Unmarshal(data []byte) error {
	var list []Friend
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.friends = make(map[string]*Friend, len(list))
	for i := range list {
		if list[i].UserHash.Equal(protocol.Invalid) {
			continue
		}
		copyFriend := list[i]
		f.friends[list[i].UserHash.String()] = &copyFriend
	}
	return nil
}

// Load reads the friend list from a JSON file (missing file = empty list).
func (f *FriendList) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Unmarshal(data)
}

// Save writes the friend list to a JSON file atomically enough for a config
// file (temp + rename is left to the state store; direct write is fine here
// because friends are changed rarely and the file is small).
func (f *FriendList) Save(path string) error {
	data, err := f.Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
