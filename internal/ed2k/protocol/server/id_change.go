package server

import (
	"bytes"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
)

// IdChange is OP_IDCHANGE. eMule's ServerSocket.cpp reads up to 20 bytes:
//
//	<clientID 4><tcpFlags 4><auxPort 4><reportedIP 4><obfuscationTCPPort 4>
//
// ReportedIP is the public IP the server sees for us. It is only meaningful
// for a LOW_ID client (for a HIGH_ID it equals ClientID) and is what
// CemuleApp::GetPublicIP() returns. CServer::GetServerKeyUDP() validates the
// UDP obfuscation key against that public IP, never against ClientID.
type IdChange struct {
	ClientID int32
	TCPFlags int32
	AuxPort  int32

	// ReportedIP is the server-observed public IP at offset 12. Zero when the
	// server sent only the legacy 12-byte form or a bogus LOW_ID-looking value.
	ReportedIP uint32
	// ObfuscationTCPPort is the TCP obfuscation port at offset 16.
	ObfuscationTCPPort uint32
}

func (i *IdChange) Get(src *bytes.Reader) error {
	clientID, err := protocol.ReadInt32(src)
	if err != nil {
		return err
	}
	i.ClientID = clientID
	if src.Len() >= 4 {
		tcpFlags, err := protocol.ReadInt32(src)
		if err != nil {
			return err
		}
		i.TCPFlags = tcpFlags
	}
	if src.Len() >= 4 {
		auxPort, err := protocol.ReadInt32(src)
		if err != nil {
			return err
		}
		i.AuxPort = auxPort
	}
	// eMule only reads these when the whole login answer is >= 20 bytes and
	// rejects a LOW_ID-looking reported IP as bogus.
	if src.Len() >= 4 {
		reportedIP, err := protocol.ReadUInt32(src)
		if err != nil {
			return err
		}
		if !isLowID(reportedIP) {
			i.ReportedIP = reportedIP
		}
	}
	if src.Len() >= 4 {
		port, err := protocol.ReadUInt32(src)
		if err != nil {
			return err
		}
		i.ObfuscationTCPPort = port
	}
	return nil
}

func (i IdChange) Put(dst *bytes.Buffer) error {
	if err := protocol.WriteInt32(dst, i.ClientID); err != nil {
		return err
	}
	if err := protocol.WriteInt32(dst, i.TCPFlags); err != nil {
		return err
	}
	if err := protocol.WriteInt32(dst, i.AuxPort); err != nil {
		return err
	}
	if err := protocol.WriteUInt32(dst, i.ReportedIP); err != nil {
		return err
	}
	return protocol.WriteUInt32(dst, i.ObfuscationTCPPort)
}

func (i IdChange) BytesCount() int { return 20 }

// isLowID mirrors eMule's ::IsLowID for the reported-IP sanity check.
func isLowID(id uint32) bool { return id < 0x01000000 }
