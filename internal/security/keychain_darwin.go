//go:build darwin

package security

import (
	"context"
	"os"
	"strings"
)

// macOS. Security.framework (`SecItemAdd` / `SecItemCopyMatching`) is the real
// API, but calling it needs cgo, which ADR-0001 forbids: a single static binary
// with CGO_ENABLED=0 must build for six targets. The supported alternative is
// /usr/bin/security, the OS-provided CLI over the same keychain, which is what
// this backend drives.
//
// Two properties matter and both were verified on a real machine:
//
//   - The secret never enters argv. Apple documents -p/-w as insecure for that
//     reason, and the "specify -w last to be prompted" form needs a TTY that a
//     daemon does not have. `security -i` reads the whole command from stdin
//     instead, so only the keychain service and the opaque id are ever visible
//     through `ps`.
//   - Exit codes are stable enough to classify: 0 = ok, 44 = errSecItemNotFound,
//     45 = errSecDuplicateItem, 2 = usage error.
const (
	securityCLIPath = "/usr/bin/security"
	// keychainService namespaces every item this daemon writes. Keep it stable:
	// changing it orphans existing credentials. It is also the item's default
	// label.
	keychainService  = "ud.credential"
	exitItemNotFound = 44
)

type keychainCLIBackend struct {
	path string
}

func newSystemBackend(opts CredentialStoreOptions) (secretBackend, error) {
	if _, err := os.Stat(securityCLIPath); err == nil {
		return &keychainCLIBackend{path: securityCLIPath}, nil
	}
	// No `security` binary: report the honest degradation instead of pretending.
	return fileOrMemory(opts)
}

func (b *keychainCLIBackend) kind() string   { return BackendMacOSKeychain }
func (b *keychainCLIBackend) scheme() string { return schemeKeychain }

// put adds or replaces the item. -U makes Put idempotent per id, which matters
// because a caller may retry after a timeout it could not classify.
func (b *keychainCLIBackend) put(ctx context.Context, id, secret string) error {
	cmd := "add-generic-password" +
		" -a " + id +
		" -s " + keychainService +
		" -U -w \"" + escapeSecurityArg(secret) + "\"\n"
	res := runCredentialCLI(ctx, b.path, []string{"-i"}, cmd)
	if res.err != nil {
		return helperError(res, "macOS keychain write")
	}
	return nil
}

func (b *keychainCLIBackend) get(ctx context.Context, id string) (string, error) {
	res := runCredentialCLI(ctx, b.path,
		[]string{"find-generic-password", "-a", id, "-s", keychainService, "-w"}, "")
	if res.err != nil {
		if res.exitCode == exitItemNotFound {
			return "", errCredentialNotFound()
		}
		// A locked login keychain lands here. Surface it: silently reporting
		// "not found" would make a locked keychain look like a dead credential.
		return "", helperError(res, "macOS keychain read")
	}
	// output is the secret plus the trailing newline `security` appends. Secrets
	// containing a newline are rejected at Put, so trimming one is lossless.
	return strings.TrimSuffix(res.stdout, "\n"), nil
}

func (b *keychainCLIBackend) delete(ctx context.Context, id string) error {
	res := runCredentialCLI(ctx, b.path,
		[]string{"delete-generic-password", "-a", id, "-s", keychainService}, "")
	if res.err != nil && res.exitCode != exitItemNotFound {
		return helperError(res, "macOS keychain delete")
	}
	return nil
}

// escapeSecurityArg quotes a value for the `security -i` command parser. It
// understands double quotes with backslash escapes and nothing else, so the two
// characters that must be escaped are backslash (first, to avoid double
// escaping) and the double quote.
func escapeSecurityArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
