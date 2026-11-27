package goed2k

import (
	"testing"

	"github.com/asdfgh129/gobitmule/internal/ed2k/protocol"
	serverproto "github.com/asdfgh129/gobitmule/internal/ed2k/protocol/server"
)

// #16: OP_SERVERSTATUS and OP_SERVERMESSAGE used to be logged and thrown away.
// They must now survive into the snapshot the console reads.
func TestServerStatusAndMessageReachSnapshot(t *testing.T) {
	session := NewSession(NewSettings())
	sc := NewServerConnection("127.0.0.1:4661", nil, session)

	status := serverproto.Status{UsersCount: 1234, FilesCount: 5678}
	statusFrame, err := sc.combiner.Pack("server.Status", &status)
	if err != nil {
		t.Fatalf("pack status: %v", err)
	}
	msg := serverproto.Message{Value: protocol.ByteContainer16FromString("welcome to the eD2k server")}
	msgFrame, err := sc.combiner.Pack("server.Message", &msg)
	if err != nil {
		t.Fatalf("pack message: %v", err)
	}

	sc.AppendIncoming(statusFrame)
	sc.AppendIncoming(msgFrame)
	if err := sc.ProcessIncoming(); err != nil {
		t.Fatalf("ProcessIncoming: %v", err)
	}

	users, files, message := sc.ServerInfo()
	if users != 1234 || files != 5678 {
		t.Fatalf("counts=%d/%d want 1234/5678", users, files)
	}
	if message != "welcome to the eD2k server" {
		t.Fatalf("message=%q want welcome banner", message)
	}
	info := sc.info()
	if info.UsersCount != 1234 || info.FilesCount != 5678 || info.LastMessage != message {
		t.Fatalf("ServerConnectionInfo did not carry server info: %+v", info)
	}
}
