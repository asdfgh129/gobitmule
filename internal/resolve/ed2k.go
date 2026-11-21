package resolve

import (
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/asdfgh129/ud/internal/core"
)

// ED2KResolver parses `ed2k://|file|name|size|hash|/` links into a KindED2K
// Resource. It performs NO network I/O: the file hash, size and name come
// straight from the link; peers/sources are discovered later by the (forked)
// eD2k Transport (WBS 16 / ADR-0030). This mirrors MagnetResolver and keeps
// the P0-frozen Resource/DDL/API schema unchanged (ADR-0013).
//
// Supported link forms:
//   ed2k://|file|<name>|<size>|<hash>|/
//   ed2k://|file|<name>|<size>|<hash>|h=<aich>|/
//   ed2k://|file|<name>|<size>|<hash>|/sources/<ip>:<port>/|/
//   ed2k://|file|<name>|<size>|<hash>|p=<sha1hash>|/  (collection root)
//
// An MD4 file hash is 16 bytes = 32 hex chars; AICH/SHA-1 roots are 20 bytes
// = 40 hex chars. All are validated for hex-ness and length before use.
type ED2KResolver struct{}

func NewED2KResolver() *ED2KResolver { return &ED2KResolver{} }

func (r *ED2KResolver) Name() string { return "ed2k" }

func (r *ED2KResolver) Match(in core.Input) bool {
	return in.Kind == core.InputED2K || strings.HasPrefix(strings.ToLower(in.Raw), "ed2k://")
}

func (r *ED2KResolver) Resolve(_ core.ResolveContext, in core.Input) (*core.Resource, error) {
	name, size, fileHash, aich, err := ParseED2KLink(in.Raw)
	if err != nil {
		return nil, err
	}
	res := &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindED2K,
		Engine: core.EngineGo,
		Files:  []core.ResourceFile{{Index: 0, Path: name, Size: size, Selected: true}},
		// eD2k has no HTTP mirror list: sources come from servers / Kad carried
		// in Identity and discovered by the Transport (ADR-0002 single scheduler).
		Mirrors:    []core.Mirror{},
		Headers:    map[string]string{},
		Credential: in.CredentialRef,
		Identity: core.ProtocolIdentity{
			ED2KFileHash: fileHash,
			AICHRoot:     aich,
		},
		Limits: core.TaskLimits{},
	}
	return res, nil
}

// ParseED2KLink extracts (name, size, md4hex, aichHex) from an ed2k:// link.
// No network access, no shell, no path traversal — the name is returned as-is
// and is later sanitized by security.PathGuard before any file is written.
func ParseED2KLink(raw string) (name string, size int64, md4hex string, aich string, err error) {
	if !strings.HasPrefix(strings.ToLower(raw), "ed2k://") {
		return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "not an ed2k:// link")
	}
	if len(raw) > 64<<10 {
		return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "ed2k link too long")
	}
	body := raw[len("ed2k://"):]
	parts := strings.Split(body, "|")
	// Canonical: "" "file" name size hash ... (leading empty before "file").
	if len(parts) < 6 || !strings.EqualFold(parts[1], "file") {
		return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "malformed ed2k file link (want ed2k://|file|...)")
	}
	name = parts[2]
	if name == "" || strings.ContainsAny(name, "\x00\r\n") {
		return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "ed2k link has invalid file name")
	}
	size, e := strconv.ParseInt(parts[3], 10, 64)
	if e != nil || size <= 0 {
		return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "ed2k link has invalid size %q", parts[3])
	}
	md4hex = strings.ToLower(parts[4])
	if !isHexLen(md4hex, 32) {
		return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "ed2k file hash must be 32 hex chars (MD4)")
	}
	// Trailing metadata: h=<aich 40 hex> and/or p=<sha1 40 hex>.
	for _, p := range parts[5:] {
		if strings.HasPrefix(strings.ToLower(p), "h=") {
			candidate := strings.ToLower(strings.TrimPrefix(p, "h="))
			if !isHexLen(candidate, 40) {
				return "", 0, "", "", core.Err(core.E_INVALID_INPUT, "ed2k AICH root must be 40 hex chars")
			}
			aich = candidate
		}
	}
	return name, size, md4hex, aich, nil
}

func isHexLen(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
