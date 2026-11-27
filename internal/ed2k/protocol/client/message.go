package client

// #6: eMule client-to-client chat (OP_MESSAGE 0x4E). A message is a 2-byte
// length followed by the text bytes; eMule sends it over the established peer
// TCP connection between friends. MAX_CLIENT_MSG_LEN (opcodes.h) caps both the
// sender and the receiver at 450 bytes.

import (
	"bytes"
	"errors"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// MaxClientMessageLen mirrors eMule's MAX_CLIENT_MSG_LEN.
const MaxClientMessageLen = 450

type Message struct {
	Text string
}

func (m *Message) Get(src *bytes.Reader) error {
	length, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	// eMule rejects a packet whose declared length does not match its body.
	if int(length) != src.Len() {
		return errors.New("chat message length mismatch")
	}
	if int(length) > MaxClientMessageLen {
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
	if len(m.Text) > MaxClientMessageLen {
		return errors.New("chat message too large")
	}
	if err := protocol.WriteUInt16(dst, uint16(len(m.Text))); err != nil {
		return err
	}
	_, err := dst.WriteString(m.Text)
	return err
}

func (m Message) BytesCount() int { return 2 + len(m.Text) }
