//go:build linux

package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asdfgh129/ud/internal/core"
)

// The Secret Service backend is exercised through a fake secret-tool on PATH:
// the point is the code WE own — attribute argv construction, secret-on-stdin,
// exit-code classification and newline trimming — not libsecret itself, which
// needs a real desktop session nobody can provide in CI. The fake helper
// therefore behaves like the real one by contract: store reads stdin, lookup
// prints the secret + newline or exits 1 silently, clear is idempotent.
func TestSecretServiceBackendWithFakeHelper(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "secret-tool")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  store) cat > /dev/null; exit 0 ;;\n" +
		"  lookup)\n" +
		"    if [ \"$5\" = \"00112233445566778899aabbccddeeff\" ]; then\n" +
		"      printf 'stored-secret\\n'; exit 0\n" +
		"    fi\n" +
		"    exit 1 ;;\n" +
		"  clear) exit 0 ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")

	be, err := newSystemBackend(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("newSystemBackend: %v", err)
	}
	ss, ok := be.(*secretServiceBackend)
	if !ok {
		t.Fatalf("backend = %T (%s), want secretServiceBackend", be, be.kind())
	}
	if ss.kind() != BackendSecretService || ss.scheme() != schemeSecret {
		t.Fatalf("kind/scheme = %q/%q", ss.kind(), ss.scheme())
	}

	s := newKeychainStore(ss)
	ctx := context.Background()

	// A well-formed but absent id must classify as not found (exit 1, silent).
	missing := core.CredentialRef(schemeSecret + "://" + refHostPath + "/ffffffffffffffffffffffffffffffff")
	if _, err := s.Get(ctx, missing); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing lookup = %v, want not-found", err)
	}

	ref, err := s.Put(ctx, `{"username":"bob","password":"s3cret"}`)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(string(ref), "secret://ud/cred/") {
		t.Fatalf("ref = %q", ref)
	}
	// The fake echoes for the fixed id only; seed it via a direct backend put
	// against that id to prove the argv ordering reaches the helper.
	if err := ss.put(ctx, "00112233445566778899aabbccddeeff", "stored-secret"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := ss.get(ctx, "00112233445566778899aabbccddeeff")
	if err != nil || got != "stored-secret" {
		t.Fatalf("get = %q, %v", got, err)
	}
	// Delete is idempotent even when the helper exits 0 for anything.
	if err := ss.delete(ctx, "00112233445566778899aabbccddeeff"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := ss.delete(ctx, "ffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatalf("delete missing = %v, want nil", err)
	}
}

// A helper that fails loudly (exit 2 here) must surface a backend error on
// every path — never a silent "not found".
func TestSecretServiceBackendHelperFailure(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "secret-tool")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")

	be, err := newSystemBackend(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("newSystemBackend: %v", err)
	}
	ss, ok := be.(*secretServiceBackend)
	if !ok {
		t.Fatalf("backend = %T", be)
	}
	ctx := context.Background()
	id, err := newCredentialID()
	if err != nil {
		t.Fatalf("newCredentialID: %v", err)
	}
	if err := ss.put(ctx, id, "x"); err == nil {
		t.Fatal("put with a failing helper must fail")
	}
	if _, err := ss.get(ctx, id); err == nil {
		t.Fatal("get with a failing helper must fail")
	}
	if err := ss.delete(ctx, id); err == nil {
		t.Fatal("delete with a failing helper must fail")
	}
}

// XDG_RUNTIME_DIR/bus is the second way a session bus is detected; without it
// (bare server, container, systemd unit) the store must degrade to the file
// backend and say so, never claim the Secret Service.
func TestSessionBusDetectionFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	// A bus socket-looking file plus a helper on PATH: the SS backend wins.
	runtime := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtime, "bus"), []byte(""), 0o600); err != nil {
		t.Fatalf("write bus file: %v", err)
	}
	helperDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(helperDir, "secret-tool"),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	os.Unsetenv("DBUS_SESSION_BUS_ADDRESS")

	be, err := newSystemBackend(CredentialStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("newSystemBackend: %v", err)
	}
	if be.kind() != BackendSecretService {
		t.Fatalf("kind = %q, want %q", be.kind(), BackendSecretService)
	}

	// Same setup but no bus file: file backend, reported as "file".
	os.RemoveAll(filepath.Join(runtime, "bus"))
	be, err = newSystemBackend(CredentialStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("newSystemBackend(no bus): %v", err)
	}
	if be.kind() != BackendFile {
		t.Fatalf("kind = %q, want %q", be.kind(), BackendFile)
	}

	// A state directory that cannot be created is a configuration error, not a
	// silent degradation. Fresh dir: the calls above already created dir/credentials.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "credentials"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if _, err := NewCredentialStore(CredentialStoreOptions{Dir: blocked}); err == nil {
		t.Fatal("NewCredentialStore with an unusable state dir must fail")
	}
}
