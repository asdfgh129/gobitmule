//go:build linux

package security

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Linux. The Secret Service API (org.freedesktop.secrets, implemented by
// gnome-keyring / KWallet) is a D-Bus protocol. Talking to it directly would
// mean either a new D-Bus dependency or a hand-rolled wire protocol, and neither
// is verifiable from this project's CI or lab hosts (see the note in
// docs/06 §9). `secret-tool`, shipped by libsecret-tools, is the upstream CLI
// over exactly that API and gives the same result with three command lines and
// no new dependency — the same shape as the macOS backend.
//
// The trade-off is explicit: this backend requires libsecret-tools plus an
// unlocked collection. When either is missing the store degrades to a 0600 file
// and CredentialBackend reports "file", never "linux-secret-service".
const (
	secretToolName = "secret-tool"
	// secretAttributeApp / secretAttributeAccount are the attribute pair
	// secret-tool uses to locate an item; both are free-form strings.
	secretAttributeApp     = "application"
	secretAttributeAccount = "account"
	secretAppValue         = "ud.credential"
	secretLabel            = "ud.credential"
	// secretToolExitNoMatch is what secret-tool returns for lookup/clear on an
	// item that does not exist.
	secretToolExitNoMatch = 1
)

type secretServiceBackend struct {
	path string
}

func newSystemBackend(opts CredentialStoreOptions) (secretBackend, error) {
	path, err := exec.LookPath(secretToolName)
	if err != nil || !sessionBusAvailable() {
		// No helper or no session bus (a bare server, a container, a systemd
		// unit without DBUS_SESSION_BUS_ADDRESS): fall back honestly.
		return fileOrMemory(opts)
	}
	return &secretServiceBackend{path: path}, nil
}

// sessionBusAvailable avoids running secret-tool only to watch it fail: without
// a session bus it can neither find the service nor autolaunch one.
func sessionBusAvailable() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "bus")); err == nil {
			return true
		}
	}
	return false
}

func (b *secretServiceBackend) kind() string   { return BackendSecretService }
func (b *secretServiceBackend) scheme() string { return schemeSecret }

// attributes are always passed as separate argv entries (never a shell string),
// and the account is a hex id, so no value here can be misinterpreted.
func (b *secretServiceBackend) attributes(id string) []string {
	return []string{
		secretAttributeApp, secretAppValue,
		secretAttributeAccount, id,
	}
}

func (b *secretServiceBackend) put(ctx context.Context, id, secret string) error {
	args := append([]string{"store", "--label", secretLabel}, b.attributes(id)...)
	// secret-tool reads the secret from stdin; argv stays free of it.
	res := runCredentialCLI(ctx, b.path, args, secret)
	if res.err != nil {
		// A locked collection fails here. Secret Service cannot be prompted
		// without a desktop session, so report instead of retrying.
		return helperError(res, "secret-tool store")
	}
	return nil
}

func (b *secretServiceBackend) get(ctx context.Context, id string) (string, error) {
	args := append([]string{"lookup"}, b.attributes(id)...)
	res := runCredentialCLI(ctx, b.path, args, "")
	if res.err != nil {
		if res.exitCode == secretToolExitNoMatch && res.stdout == "" && res.stderr == "" {
			return "", errCredentialNotFound()
		}
		return "", helperError(res, "secret-tool lookup")
	}
	return strings.TrimSuffix(res.stdout, "\n"), nil
}

func (b *secretServiceBackend) delete(ctx context.Context, id string) error {
	args := append([]string{"clear"}, b.attributes(id)...)
	res := runCredentialCLI(ctx, b.path, args, "")
	// Deleting an absent item satisfies the caller's intent.
	if res.err != nil && res.exitCode != secretToolExitNoMatch {
		return helperError(res, "secret-tool clear")
	}
	return nil
}
