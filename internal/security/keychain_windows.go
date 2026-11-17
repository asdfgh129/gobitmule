//go:build windows

package security

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/asdfgh129/ud/internal/core"
)

// Windows. Credential Manager (CredWrite/CredRead) would be the obvious
// keychain, but its generic-credential blobs are capped at 5x512 bytes, which a
// PEM private key blows past. DPAPI (CryptProtectData) has no such limit and is
// the primitive Credential Manager itself is built on: the OS derives the key
// from the logged-on user's profile, so the ciphertext is useless to any other
// account and to a copy of the disk.
//
// Everything here is a pure syscall into crypt32.dll, so CGO_ENABLED=0 and the
// single-binary constraint (ADR-0001) hold. The ciphertext lives in a file; the
// file is what makes the secret survive a restart, DPAPI is what makes it
// unreadable to anyone else.
var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32.NewProc("LocalFree")
)

// cryptProtectUIForbidden must always be set: without it DPAPI is allowed to
// raise a GUI prompt, which a service or a headless session turns into a hang.
const cryptProtectUIForbidden = 0x1

// dataBlob mirrors the Win32 DATA_BLOB. Field order and sizes are fixed by the
// ABI; the trailing padding is implicit.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

type dpapiBackend struct {
	dir string
}

func newSystemBackend(opts CredentialStoreOptions) (secretBackend, error) {
	// DPAPI protects the payload but still needs somewhere to keep it; with no
	// state directory there is nothing to protect.
	if opts.Dir == "" {
		return newMemoryBackend(), nil
	}
	dir := filepath.Join(opts.Dir, "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, core.Err(core.E_INTERNAL, "create credential directory: %v", err)
	}
	return &dpapiBackend{dir: dir}, nil
}

func (b *dpapiBackend) kind() string   { return BackendWindowsDPAPI }
func (b *dpapiBackend) scheme() string { return schemeDPAPI }

func (b *dpapiBackend) path(id string) string { return filepath.Join(b.dir, id+".bin") }

func (b *dpapiBackend) put(_ context.Context, id, secret string) error {
	sealed, err := dpapiProtect([]byte(secret))
	if err != nil {
		return core.Err(core.E_INTERNAL, "DPAPI protect: %v", err)
	}
	final := b.path(id)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		os.Remove(tmp)
		return core.Err(core.E_INTERNAL, "write credential file: %v", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return core.Err(core.E_INTERNAL, "publish credential file: %v", err)
	}
	return nil
}

func (b *dpapiBackend) get(_ context.Context, id string) (string, error) {
	sealed, err := os.ReadFile(b.path(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errCredentialNotFound()
		}
		return "", core.Err(core.E_INTERNAL, "read credential file: %v", err)
	}
	plain, err := dpapiUnprotect(sealed)
	if err != nil {
		// Ciphertext written by a different Windows account (or after a profile
		// reset) cannot be opened. Say so plainly instead of returning garbage.
		return "", core.Err(core.E_INTERNAL,
			"credential was sealed for another Windows account: %v", err)
	}
	return string(plain), nil
}

func (b *dpapiBackend) delete(_ context.Context, id string) error {
	if err := os.Remove(b.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return core.Err(core.E_INTERNAL, "delete credential file: %v", err)
	}
	return nil
}

func dpapiProtect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("empty payload")
	}
	in := dataBlob{cbData: uint32(len(plain)), pbData: &plain[0]}
	var out dataBlob
	r, _, errno := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // description
		0, // optional entropy
		0, 0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, callError(errno)
	}
	return consumeBlob(out)
}

func dpapiUnprotect(sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, errors.New("empty ciphertext")
	}
	in := dataBlob{cbData: uint32(len(sealed)), pbData: &sealed[0]}
	var out dataBlob
	r, _, errno := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, // description out
		0, // optional entropy
		0, 0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, callError(errno)
	}
	return consumeBlob(out)
}

// consumeBlob copies the LocalAlloc'd output into Go memory and releases it.
func consumeBlob(b dataBlob) ([]byte, error) {
	defer procLocalFree.Call(uintptr(unsafe.Pointer(b.pbData)))
	if b.pbData == nil || b.cbData == 0 {
		return nil, errors.New("DPAPI returned an empty blob")
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out, nil
}

func callError(errno error) error {
	if errno == nil || errors.Is(errno, syscall.Errno(0)) {
		return errors.New("DPAPI call failed")
	}
	return errno
}
