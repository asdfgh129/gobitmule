package goed2k

import (
	"errors"
	"strings"
	"testing"
)

// A server.met holds many candidates. Reaching any one of them is enough for
// source discovery; a single dead entry must not fail the whole batch. When
// none connect, the last failure is surfaced so callers fail closed.
func TestServerConnectFailure(t *testing.T) {
	dead := errors.New("dial tcp 45.82.80.155:5687: connectex: timed out")

	tests := []struct {
		name    string
		lastErr error
		wantErr bool
		wantMsg string
	}{
		{name: "all failed", lastErr: dead, wantErr: true, wantMsg: dead.Error()},
		{name: "no candidates", lastErr: nil, wantErr: true, wantMsg: "no server address provided"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := serverConnectFailure(tt.lastErr)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}
