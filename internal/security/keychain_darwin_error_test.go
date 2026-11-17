//go:build darwin

package security

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

func msgContains(s, sub string) bool { return strings.Contains(s, sub) }

// Failure mapping must not rely on the real keychain: a helper that exits 1 —
// neither 0 (ok) nor 44 (item not found) — must surface a backend error on
// every path instead of silently reporting "credential not found". /usr/bin/head
// is used because it always exits non-zero when handed these arguments, without
// touching the keychain at all.
func TestMacOSKeychainErrorMapping(t *testing.T) {
	if _, err := os.Stat("/usr/bin/head"); err != nil {
		t.Skip("/usr/bin/head not present: NOT_EXECUTED")
	}
	be := &keychainCLIBackend{path: "/usr/bin/head"}
	ctx := context.Background()
	id, err := newCredentialID()
	if err != nil {
		t.Fatalf("newCredentialID: %v", err)
	}

	err = be.put(ctx, id, "x")
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.E_INTERNAL {
		t.Fatalf("put = %v, want E_INTERNAL", err)
	}
	if msg := ce.Message; !msgContains(msg, "macOS keychain write failed (exit") {
		t.Fatalf("message = %q", msg)
	}

	// A non-44 exit on read is NOT "not found": a locked keychain must look
	// like a failure, never like a missing credential.
	_, err = be.get(ctx, id)
	if err == nil {
		t.Fatal("get with a failing helper must fail")
	}
	if !errors.As(err, &ce) || ce.Code != core.E_INTERNAL {
		t.Fatalf("get = %v, want E_INTERNAL", err)
	}

	if err := be.delete(ctx, id); err == nil {
		t.Fatal("delete with a failing helper must fail")
	}
}
