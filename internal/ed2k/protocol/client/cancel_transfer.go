package client

import "github.com/asdfgh129/ud/internal/ed2k/protocol"

type CancelTransfer struct {
	protocol.WithoutDataPacket
}
