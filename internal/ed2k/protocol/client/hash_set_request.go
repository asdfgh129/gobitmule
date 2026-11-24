package client

import (
	"bytes"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

type HashSetRequest struct {
	Hash protocol.Hash
}

func (h *HashSetRequest) Get(src *bytes.Reader) error {
	hash, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	h.Hash = hash
	return nil
}

func (h HashSetRequest) Put(dst *bytes.Buffer) error {
	return protocol.WriteHash(dst, h.Hash)
}

func (h HashSetRequest) BytesCount() int {
	return 16
}
