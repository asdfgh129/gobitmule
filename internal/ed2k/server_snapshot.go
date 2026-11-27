package goed2k

// Read-only view of the engine's server connections, used by the Web console's
// server list. The engine remains the sole owner of connection state.

type ServerConnectionInfo struct {
	Identifier        string `json:"identifier"`
	Address           string `json:"address"`
	Connected         bool   `json:"connected"`
	HandshakeComplete bool   `json:"handshakeComplete"`
	ClientID          int32  `json:"clientId"`
	LastPing          int64  `json:"lastPing"`
	// #16: server banner/counts from OP_SERVERMESSAGE / OP_SERVERSTATUS.
	LastMessage string `json:"lastMessage"`
	UsersCount  int32  `json:"usersCount"`
	FilesCount  int32  `json:"filesCount"`
	// OP_SERVERIDENT identity and UDP capabilities.
	Name     string `json:"name,omitempty"`
	UDPFlags uint32 `json:"udpFlags,omitempty"`
}

func (c *ServerConnection) info() ServerConnectionInfo {
	info := ServerConnectionInfo{
		Identifier:        c.identifier,
		HandshakeComplete: c.IsHandshakeCompleted(),
		ClientID:          c.clientID,
		LastPing:          c.lastPingTime,
		Name:              c.ServerName(),
		UDPFlags:          c.UDPFlags(),
	}
	if c.address != nil {
		info.Address = c.address.String()
	}
	info.Connected = c.socket != nil && !c.IsDisconnecting()
	info.UsersCount, info.FilesCount, info.LastMessage = c.ServerInfo()
	return info
}

// ServerSnapshot returns a stable copy of all known server connections.
func (s *Session) ServerSnapshot() []ServerConnectionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ServerConnectionInfo, 0, len(s.serverConnections))
	for _, sc := range s.serverConnections {
		if sc != nil {
			out = append(out, sc.info())
		}
	}
	return out
}
