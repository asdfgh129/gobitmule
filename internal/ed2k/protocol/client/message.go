package client

// #6: eMule client-to-client chat (OP_MESSAGE 0x4E). A message is a 2-byte
// length followed by the text bytes; eMule sends it over the established peer
// TCP connection between friends.

import (
	"bytes"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

type Message struct {
	Text string
}

func (m *Message) Get(src *bytes.Reader) error {
	length, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	if int(length) > 8192 {
		return errors.New("chat message too large")
	}
	raw := make([]byte, int(length))
	if _, err := src.Read(raw); err != nil {
		return err
	}
	m.Text = string(raw)
	return nil
}

func (m Message) Put(dst *bytes.Buffer) error {
	if len(m.Text) > 8192 {
		return errors.New("chat message too large")
	}
	if err := protocol.WriteUInt16(dst, uint16(len(m.Text))); err != nil {
		return err
	}
	_, err := dst.WriteString(m.Text)
	return err
}

func (m Message) BytesCount() int { return 2 + len(m.Text) }
