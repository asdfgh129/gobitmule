package resolve

// TGResolver implements the Telegram link classification (WBS 15 /
// FR-SRC-04, ADR-0029). Classification is offline: link shapes are parsed and
// normalised to the internal tg://ud mirror form; message metadata (size,
// filename) comes from the transport's Probe over MTProto.

import (
	"net/url"
	"strings"

	"github.com/asdfgh129/ud/internal/core"
	tgtransport "github.com/asdfgh129/ud/internal/transport/tg"
)

// TGResolver classifies Telegram message links.
type TGResolver struct{}

// NewTGResolver builds the resolver.
func NewTGResolver() *TGResolver { return &TGResolver{} }

func (r *TGResolver) Name() string { return "telegram" }

// Match claims URL inputs that PARSE as Telegram message links (t.me /
// telegram.me hosts or the tg:// scheme). Host heuristics alone would claim
// "https://t.me/durov" (no message id) and bounce between resolvers — the
// parse is the match (ADR-0029).
func (r *TGResolver) Match(in core.Input) bool {
	if in.Kind != core.InputURL {
		return false
	}
	scheme := strings.ToLower(core.SchemeOf(in.Raw))
	if scheme != "tg" {
		host := strings.ToLower(linkHost(in.Raw))
		if host != "t.me" && host != "telegram.me" {
			return false
		}
	}
	_, err := tgtransport.ParseLink(in.Raw)
	return err == nil
}

func linkHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (r *TGResolver) Resolve(rc core.ResolveContext, in core.Input) (*core.Resource, error) {
	ref, err := tgtransport.ParseLink(in.Raw)
	if err != nil {
		return nil, err
	}
	return &core.Resource{
		ID:     "res-" + string(core.NewID()),
		Kind:   core.KindSingle,
		Engine: core.EngineGo,
		Files: []core.ResourceFile{{
			Index: 0,
			// Placeholder: the transport's Probe refines the filename from
			// the document attributes over MTProto.
			Path:     "tg-" + ref.Channel + "-" + itoa(ref.MsgID) + ".bin",
			Selected: true,
		}},
		Mirrors: []core.Mirror{{URL: tgtransport.Normalize(ref), Priority: 1, Weight: 1}},
		Headers: map[string]string{},
		// The MTProto session travels as a credential reference (FR-SRC-06):
		// the blob lives in the OS keychain, never in SQLite or logs.
		Credential: rc.Credential,
		Identity: core.ProtocolIdentity{
			VariantID: "tg:" + ref.Channel + "/" + itoa(ref.MsgID),
		},
		Limits: core.TaskLimits{},
	}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
