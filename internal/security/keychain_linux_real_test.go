//go:build linux && keyringreal

package security

// Real-machine evidence for the Linux Secret Service (secret-tool) backend.
// The default Linux test file drives a fake helper on PATH, which proves OUR
// argv/stdin/exit-code handling but not the contract with libsecret, D-Bus and
// an unlocked keyring. These tests only compile with -tags keyringreal and are
// meant to be run on a host that actually has all three. They are deliberately
// kept out of CI, where no desktop session exists.
//
// Run (Ubuntu with libsecret-tools + gnome-keyring):
//
//	dbus-run-session -- bash -c '
//	  eval "$(printf "\n" | gnome-keyring-daemon --unlock --components=secrets)"
//	  go test -count=1 -tags keyringreal ./internal/security -run RealMachine -v'

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

func requireRealSecretService(t *testing.T) string {
	t.Helper()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("no D-Bus session bus: NOT_EXECUTED on this host")
	}
	path, err := exec.LookPath(secretToolName)
	if err != nil {
		t.Skipf("secret-tool not on PATH: NOT_EXECUTED on this host (%v)", err)
	}
	return path
}

// The same selection path POST /credentials uses: with a state directory and
// a live Secret Service the store must report linux-secret-service, mint
// secret:// refs and round-trip a realistic JSON credential verbatim.
func TestLinuxSecretServiceRealMachineStoreEndToEnd(t *testing.T) {
	requireRealSecretService(t)
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	if got := s.CredentialBackend(); got != BackendSecretService {
		t.Fatalf("CredentialBackend = %q, want %q", got, BackendSecretService)
	}
	ctx := context.Background()
	secret := `{"username":"alice","password":"p@ss 密码 \"quoted\" #hash"}`
	ref, err := s.Put(ctx, secret)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), ref) })
	if !strings.HasPrefix(string(ref), "secret://ud/cred/") {
		t.Fatalf("ref = %q, want the secret://ud/cred/ prefix", ref)
	}
	if strings.Contains(string(ref), "p@ss") {
		t.Fatal("secret leaked into the credential reference")
	}
	got, err := s.Get(ctx, ref)
	if err != nil || got != secret {
		t.Fatalf("Get = %q, %v; want %q", got, err, secret)
	}
}

// Independently of our Get, ask the real secret-tool CLI for the item using
// the same attribute pair. This proves the secret actually landed in the OS
// keyring, and that Delete removes it from there.
func TestLinuxSecretServiceRealMachineItemInKeyring(t *testing.T) {
	tool := requireRealSecretService(t)
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	ctx := context.Background()
	secret := "real-keyring-evidence"
	ref, err := s.Put(ctx, secret)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), ref) })
	id := strings.TrimPrefix(string(ref), "secret://ud/cred/")

	lookup := func() (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, tool, "lookup",
			secretAttributeApp, secretAppValue, secretAttributeAccount, id).Output()
		return strings.TrimSuffix(string(out), "\n"), err
	}
	if value, err := lookup(); err != nil || value != secret {
		t.Fatalf("secret-tool lookup = %q, %v; want %q", value, err, secret)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if value, err := lookup(); err == nil {
		t.Fatalf("secret-tool lookup after Delete returned %q; item still in keyring", value)
	}
}

// A well-formed but unknown id maps to the documented not-found error (the
// real secret-tool exits 1 with no output); malformed refs never reach a CLI;
// Delete stays idempotent.
func TestLinuxSecretServiceRealMachineMissingAndIdempotentDelete(t *testing.T) {
	requireRealSecretService(t)
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	ctx := context.Background()
	missing := core.CredentialRef("secret://ud/cred/ffffffffffffffffffffffffffffffff")
	if _, err := s.Get(ctx, missing); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Get(missing) = %v, want not-found", err)
	}
	foreign := core.CredentialRef("dpapi://ud/cred/00112233445566778899aabbccddeeff")
	if _, err := s.Get(ctx, foreign); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Get(foreign) = %v, want not-found", err)
	}
	if err := s.Delete(ctx, missing); err != nil {
		t.Fatalf("Delete(missing) = %v, want nil", err)
	}
	if err := s.Delete(ctx, missing); err != nil {
		t.Fatalf("second Delete(missing) = %v, want nil", err)
	}
}

// Two ids are independent items: deleting one must not disturb the other.
func TestLinuxSecretServiceRealMachineIsolation(t *testing.T) {
	requireRealSecretService(t)
	s, err := NewCredentialStore(CredentialStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	ctx := context.Background()
	first, err := s.Put(ctx, "first-secret")
	if err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), first) })
	second, err := s.Put(ctx, "second-secret")
	if err != nil {
		t.Fatalf("Put(second): %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), second) })
	if first == second {
		t.Fatal("two Put calls minted the same reference")
	}
	if err := s.Delete(ctx, first); err != nil {
		t.Fatalf("Delete(first): %v", err)
	}
	if got, err := s.Get(ctx, second); err != nil || got != "second-secret" {
		t.Fatalf("Get(second) after Delete(first) = %q, %v", got, err)
	}
}

// The secret must travel on stdin, never in argv where local users could see
// it in `ps`. This pins the mechanism, not just the round-trip outcome.
func TestLinuxSecretServiceRealMachineSecretNotInArgv(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("keychain_linux.go"))
	if err != nil {
		t.Fatalf("read keychain_linux.go: %v", err)
	}
	code := string(src)
	if !strings.Contains(code, `runCredentialCLI(ctx, b.path, args, secret)`) {
		t.Fatal("the store path must hand the secret to runCredentialCLI as stdin")
	}
	if strings.Contains(code, `append(args, secret)`) {
		t.Fatal("the secret must never be appended to the helper argv")
	}
}
