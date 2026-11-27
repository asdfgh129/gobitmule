package client

// OP_CALLBACK (0x99, EMuleProt) is how a Kad buddy tells a firewalled peer that
// somebody wants to reach it. It is the client-to-client half of the Kad
// callback dance: a downloader sends KADEMLIA_CALLBACK_REQ to the source's
// buddy, and the buddy forwards <check><file><requester ip><requester port>
// over the TCP connection it already has with the source.
//
// Wire form (BaseClient.cpp CUpDownClient::ProcessCallbackReq):
//
//	<check hash 16><file hash 16><ip 4><port 2>

import (
	"bytes"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

type Callback struct {
	Check protocol.Hash
	File  protocol.Hash
	IP    uint32
	Port  uint16
}

func (c *Callback) Get(src *bytes.Reader) error {
	check, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	file, err := protocol.ReadHash(src)
	if err != nil {
		return err
	}
	ip, err := protocol.ReadUInt32(src)
	if err != nil {
		return err
	}
	port, err := protocol.ReadUInt16(src)
	if err != nil {
		return err
	}
	c.Check, c.File, c.IP, c.Port = check, file, ip, port
	return nil
}

func (c Callback) Put(dst *bytes.Buffer) error {
	if err := protocol.WriteHash(dst, c.Check); err != nil {
		return err
	}
	if err := protocol.WriteHash(dst, c.File); err != nil {
		return err
	}
	if err := protocol.WriteUInt32(dst, c.IP); err != nil {
		return err
	}
	return protocol.WriteUInt16(dst, c.Port)
}

func (c Callback) BytesCount() int { return 16 + 16 + 4 + 2 }

// Endpoint returns the requester's TCP address.
func (c Callback) Endpoint() protocol.Endpoint {
	return protocol.NewEndpoint(int32(c.IP), int(c.Port))
}
