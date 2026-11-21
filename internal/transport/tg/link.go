package tgtransport

// Telegram link parsing (WBS 15 / FR-SRC-04, ADR-0029). Supported shapes:
//
//	https://t.me/<channel>/<msgID>          public channel / user post
//	https://t.me/c/<internalID>/<msgID>     private channel (internal ID)
//	tg://resolve?domain=<channel>&post=<id> app deep link, public
//	tg://privatepost?channel=<id>&post=<id> app deep link, private
//
// Every supported link normalises to the INTERNAL mirror form
// `tg://ud/link?...` (or `tg://ud/private?...`): no http(s) transport can
// claim it, the generic SSRF Guard never sees a Telegram DC address, and
// dispatch is unambiguous.

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// LinkRef is one parsed Telegram message reference.
type LinkRef struct {
	Channel string // public domain, or internal numeric ID for private links
	MsgID   int
	Private bool
}

const (
	// maxLinkBytes bounds the accepted input the same way as the SSRF Guard.
	maxLinkBytes = 8192
)

// Sentinel errors: stable classification without string matching.
var (
	ErrNotTGLink    = errors.New("not a telegram message link")
	ErrPrivateLinks = errors.New("private channel links need the account's channel access hash via an authenticated session (P3)")
)

// ParseLink parses one supported Telegram message link.
func ParseLink(raw string) (LinkRef, error) {
	var ref LinkRef
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxLinkBytes {
		return ref, fmt.Errorf("%w: length outside 1..%d", ErrNotTGLink, maxLinkBytes)
	}
	if strings.ContainsAny(raw, "\x00\r\n\t ") {
		return ref, fmt.Errorf("%w: contains whitespace or control characters", ErrNotTGLink)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ref, fmt.Errorf("%w: %v", ErrNotTGLink, err)
	}
	q := u.Query()
	switch strings.ToLower(u.Scheme) {
	case "tg":
		// In tg:// deep links the "path" segment parses as the HOST
		// (tg://resolve → host=resolve, path="").
		tgPath := strings.TrimPrefix(strings.ToLower(u.Hostname())+strings.TrimSuffix(u.Path, "/"), "/")
		switch tgPath {
		case "resolve":
			ref.Channel = strings.ToLower(strings.TrimSpace(q.Get("domain")))
			ref.MsgID = atoiDefault(q.Get("post"))
		case "privatepost":
			ref.Channel = strings.TrimSpace(q.Get("channel"))
			ref.MsgID = atoiDefault(q.Get("post"))
			ref.Private = true
		default:
			return ref, fmt.Errorf("%w: unsupported tg path %q", ErrNotTGLink, tgPath)
		}
	case "https", "http":
		host := strings.ToLower(u.Hostname())
		if host != "t.me" && host != "telegram.me" {
			return ref, fmt.Errorf("%w: host %q", ErrNotTGLink, host)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 3 && parts[0] == "c" {
			// Private channel: /c/<internalID>/<msgID>.
			ref.Private = true
			ref.Channel = strings.TrimSpace(parts[1])
			ref.MsgID = atoiDefault(parts[2])
		} else if len(parts) == 2 {
			// Public: /<channel>/<msgID>; the app deep-link equivalent.
			ref.Channel = strings.ToLower(strings.TrimSpace(parts[0]))
			ref.MsgID = atoiDefault(parts[1])
		} else {
			return ref, fmt.Errorf("%w: t.me path %q must be /<channel>/<msg> or /c/<id>/<msg>", ErrNotTGLink, u.Path)
		}
	default:
		return ref, fmt.Errorf("%w: scheme %q", ErrNotTGLink, u.Scheme)
	}
	if ref.Channel == "" {
		return ref, fmt.Errorf("%w: empty channel", ErrNotTGLink)
	}
	if ref.MsgID <= 0 {
		return ref, fmt.Errorf("%w: message id must be a positive integer", ErrNotTGLink)
	}
	if ref.Private {
		if !isDigits(ref.Channel) {
			return ref, fmt.Errorf("%w: private channel id %q must be numeric", ErrNotTGLink, ref.Channel)
		}
	} else if !isDomainish(ref.Channel) {
		return ref, fmt.Errorf("%w: channel %q is not a plausible username", ErrNotTGLink, ref.Channel)
	}
	return ref, nil
}

// Normalize renders the internal mirror form for one parsed link.
func Normalize(ref LinkRef) string {
	if ref.Private {
		return fmt.Sprintf("tg://ud/private?channel=%s&post=%d", url.QueryEscape(ref.Channel), ref.MsgID)
	}
	return fmt.Sprintf("tg://ud/link?domain=%s&post=%d", url.QueryEscape(ref.Channel), ref.MsgID)
}

// ParseNormalized re-parses an internal mirror URL back into a LinkRef.
func ParseNormalized(raw string) (LinkRef, error) {
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "tg" || strings.ToLower(u.Host) != "ud" {
		return LinkRef{}, fmt.Errorf("%w: not an internal tg mirror URL", ErrNotTGLink)
	}
	q := u.Query()
	ref := LinkRef{MsgID: atoiDefault(q.Get("post"))}
	if strings.TrimPrefix(strings.ToLower(u.Path), "/") == "private" {
		ref.Private = true
		ref.Channel = strings.TrimSpace(q.Get("channel"))
	} else {
		ref.Channel = strings.ToLower(strings.TrimSpace(q.Get("domain")))
	}
	if ref.Channel == "" || ref.MsgID <= 0 {
		return LinkRef{}, fmt.Errorf("%w: mirror URL missing channel/post", ErrNotTGLink)
	}
	return ref, nil
}

func atoiDefault(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isDomainish accepts Telegram usernames: letters, digits, underscores, 4-64
// chars (Telegram's own rule), starting with a letter.
func isDomainish(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for i, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		if i > 0 && c >= '0' && c <= '9' {
			ok = true
		}
		if !ok {
			return false
		}
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
