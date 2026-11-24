package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// ADR-0015 decision ④ requires credentials (Cookie, Telegram session, proxy
// password, PT passkey) to live in the OS secret store, with only an opaque
// credential_ref left in SQLite, events, logs and REST responses.
//
// The zero-cgo constraint (ADR-0001) rules out Security.framework / libsecret
// headers, so every backend below is built from one of three shapes:
//
//   - an OS-provided CLI that the platform itself signs and maintains
//     (macOS /usr/bin/security, Linux secret-tool from libsecret-tools);
//   - a pure Go syscall into the platform API (Windows CryptProtectData);
//   - a private 0600 file when neither is available — explicitly NOT a
//     keychain and reported as such through CredentialBackend.
//
// Backend identifiers are the wire values of GET /capabilities.credentials
// (docs/07 §2). They must never claim more than the backend really does:
// "file" means "no OS keychain, file permissions only".
const (
	BackendMemory        = "memory"
	BackendFile          = "file"
	BackendMacOSKeychain = "macos-keychain"
	BackendWindowsDPAPI  = "windows-dpapi"
	BackendSecretService = "linux-secret-service"
)

// Ref schemes. Every ref has the shape "<scheme>://ud/cred/<id>" so a ref is
// self-describing: a ref minted by one backend can never be resolved by
// another (the prefix check fails) and cannot be replayed across machines.
const (
	schemeMemory   = "mem"
	schemeKeychain = "keychain"
	schemeSecret   = "secret"
	schemeDPAPI    = "dpapi"
	schemeFile     = "file"
	refHostPath    = "ud/cred"
)

const (
	// credentialIDBytes is the entropy of the opaque handle; 128 bits makes an
	// accidental collision impossible in practice, and the hex encoding also
	// guarantees the id can never contain a path separator.
	credentialIDBytes = 16

	// maxKeychainSecretBytes bounds what we hand to an OS helper. A credential
	// blob is a small JSON object (tputil.MaxCredentialBytes is 1MiB, but a
	// 1MiB private key through a CLI line protocol is not a real use case), and
	// a bound keeps a hostile POST /credentials from stuffing an unbounded argv
	// or line into the keychain helper.
	maxKeychainSecretBytes = 64 << 10
)

// CredentialStoreOptions configures NewCredentialStore.
type CredentialStoreOptions struct {
	// Dir is the daemon state directory. Backends that need somewhere to keep
	// their payload (the 0600 file, the DPAPI ciphertext) create a private
	// "credentials" subdirectory below it. When Dir is empty those backends are
	// unavailable and the store degrades to memory.
	Dir string
}

// secretBackend is the per-platform mechanism. It deals in bare ids; the ref
// scheme, the id format and the wire errors live in KeychainStore so every
// backend behaves identically at the CredentialStore boundary.
type secretBackend interface {
	// kind is the value reported by CredentialBackend and GET /capabilities.
	kind() string
	// scheme is the ref prefix this backend mints.
	scheme() string
	put(ctx context.Context, id, secret string) error
	get(ctx context.Context, id string) (string, error)
	delete(ctx context.Context, id string) error
}

// KeychainStore is the OS-backed implementation of core.CredentialStore.
//
// Secrets are never written to SQLite, events, logs or REST responses; the
// store only ever returns them to a transport that asked for a specific ref.
type KeychainStore struct {
	be     secretBackend
	prefix string
}

func newKeychainStore(be secretBackend) *KeychainStore {
	return &KeychainStore{be: be, prefix: be.scheme() + "://" + refHostPath + "/"}
}

// NewCredentialStore picks the strongest backend the current machine can
// actually run. It returns an error only when a configured state directory
// exists but is unusable; "no keychain on this host" is not an error, it is a
// documented degradation reported by CredentialBackend.
func NewCredentialStore(opts CredentialStoreOptions) (*KeychainStore, error) {
	be, err := newSystemBackend(opts)
	if err != nil {
		return nil, err
	}
	return newKeychainStore(be), nil
}

// NewMemoryCredentialStore is the in-process fallback: nothing survives a
// restart. It exists for tests, for `ud --dry-run` style usage and for hosts
// where no persistent backend is available.
func NewMemoryCredentialStore() *KeychainStore {
	return newKeychainStore(newMemoryBackend())
}

// CredentialBackend names the active mechanism (ADR-0015 ④). GET /capabilities
// publishes it so an operator can tell "OS keychain" from "0600 file".
func (s *KeychainStore) CredentialBackend() string { return s.be.kind() }

func (s *KeychainStore) Put(ctx context.Context, secret string) (core.CredentialRef, error) {
	if err := validateKeychainSecret(secret); err != nil {
		return "", err
	}
	id, err := newCredentialID()
	if err != nil {
		return "", core.Err(core.E_INTERNAL, "generate credential id: %v", err)
	}
	if err := s.be.put(ctx, id, secret); err != nil {
		return "", err
	}
	return core.CredentialRef(s.prefix + id), nil
}

func (s *KeychainStore) Get(ctx context.Context, ref core.CredentialRef) (string, error) {
	id, err := s.idFromRef(ref)
	if err != nil {
		return "", err
	}
	secret, err := s.be.get(ctx, id)
	if err != nil {
		return "", err
	}
	// A stored empty value is not a credential; treat it as absent so callers
	// never proceed with a blank password.
	if secret == "" {
		return "", errCredentialNotFound()
	}
	return secret, nil
}

// Delete is idempotent: removing an unknown ref succeeds, because the caller's
// intent ("this secret must no longer exist") is already satisfied.
func (s *KeychainStore) Delete(ctx context.Context, ref core.CredentialRef) error {
	id, err := s.idFromRef(ref)
	if err != nil {
		return nil
	}
	return s.be.delete(ctx, id)
}

// idFromRef rejects any ref this backend did not mint, before it can reach a
// CLI, a file path or a D-Bus attribute.
func (s *KeychainStore) idFromRef(ref core.CredentialRef) (string, error) {
	if ref == "" || !strings.HasPrefix(string(ref), s.prefix) {
		return "", errCredentialNotFound()
	}
	id := string(ref)[len(s.prefix):]
	if !validCredentialID(id) {
		return "", errCredentialNotFound()
	}
	return id, nil
}

// errCredentialNotFound matches the message the P0 memory store has always
// returned (docs/07 §6 E_INVALID_INPUT), so existing clients and tests keep
// their behaviour. The ref itself is never echoed back.
func errCredentialNotFound() error {
	return core.Err(core.E_INVALID_INPUT, "credential reference not found")
}

func newCredentialID() (string, error) {
	var b [credentialIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// validCredentialID accepts exactly the hex ids newCredentialID produces. The
// strict shape is a security boundary: an id reaches a shell-free exec argv, a
// file path and a keychain attribute, so nothing outside [0-9a-f] may pass.
func validCredentialID(id string) bool {
	if len(id) != credentialIDBytes*2 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validateKeychainSecret applies the limits the OS helpers impose. Control
// characters are rejected because every CLI backend transports the secret over
// a line-based channel (macOS "security -i" reads commands line by line), where
// a raw newline would silently truncate the credential. JSON credential blobs
// escape newlines inside strings, so valid input never contains one.
func validateKeychainSecret(secret string) error {
	if secret == "" {
		return core.Err(core.E_INVALID_INPUT, "empty credential")
	}
	if len(secret) > maxKeychainSecretBytes {
		return core.Err(core.E_INVALID_INPUT,
			"credential must be at most %d bytes", maxKeychainSecretBytes)
	}
	for i := 0; i < len(secret); i++ {
		if c := secret[i]; c < 0x20 || c == 0x7f {
			return core.Err(core.E_INVALID_INPUT,
				"credential contains control characters; they cannot be stored in the OS keychain")
		}
	}
	return nil
}

// fileOrMemory is the degradation path shared by the platform files when the
// preferred mechanism is absent.
func fileOrMemory(opts CredentialStoreOptions) (secretBackend, error) {
	if opts.Dir == "" {
		return newMemoryBackend(), nil
	}
	return newFileBackend(opts.Dir)
}

// ---------- in-process backend ----------

type memoryBackend struct {
	mu    sync.RWMutex
	items map[string]string
}

func newMemoryBackend() *memoryBackend { return &memoryBackend{items: map[string]string{}} }

func (b *memoryBackend) kind() string   { return BackendMemory }
func (b *memoryBackend) scheme() string { return schemeMemory }

func (b *memoryBackend) put(_ context.Context, id, secret string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[id] = secret
	return nil
}

func (b *memoryBackend) get(_ context.Context, id string) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.items[id]
	if !ok {
		return "", errCredentialNotFound()
	}
	return v, nil
}

func (b *memoryBackend) delete(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, id)
	return nil
}

// errBackendUnavailable is returned when a platform file decides it cannot
// offer even the file fallback.
var errBackendUnavailable = errors.New("no credential backend available on this host")
