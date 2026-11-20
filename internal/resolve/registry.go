// Package resolve hosts Resolver registration and the P0 HTTP direct resolver.
package resolve

import (
	"sync"

	"github.com/asdfgh129/ud/internal/core"
)

// Registry resolves inputs by priority: the first matching Resolver wins.
// Built-in resolvers are registered before plugins and can never be overridden
// by a plugin (docs/05 §10).
type Registry struct {
	mu        sync.RWMutex
	resolvers []core.Resolver
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) Register(x core.Resolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolvers = append(r.resolvers, x)
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.resolvers))
	for _, x := range r.resolvers {
		out = append(out, x.Name())
	}
	return out
}

// Resolve returns a complete Resource or a classified error.
//
// Forced stream routing (ADR-0023 / ADR-0024): a `{"hls": true}` or
// `{"dash": true}` task option dispatches straight to that resolver even
// when the URL lacks the .m3u8 / .mpd extension (tokenized CDN URLs).
// Resolver.Match cannot see task options — its signature is part of the
// frozen core contract — so the registry performs the override centrally,
// before the normal first-match chain runs. The forced resolver still
// validates the scheme and the SSRF guard itself.
func (r *Registry) Resolve(ctx core.ResolveContext, in core.Input) (*core.Resource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name, ok := forcedStreamResolver(ctx.Options); ok {
		for _, x := range r.resolvers {
			if x.Name() == name {
				return x.Resolve(ctx, in)
			}
		}
	}
	for _, x := range r.resolvers {
		if x.Match(in) {
			return x.Resolve(ctx, in)
		}
	}
	return nil, core.Err(core.E_RESOLVE_FAILED, "no resolver registered for %s", in.Kind)
}

// forcedStreamResolver maps the task options to a resolver override.
func forcedStreamResolver(opts map[string]any) (string, bool) {
	if v, ok := opts[OptHLSForce].(bool); ok && v {
		return "hls", true
	}
	if v, ok := opts[OptDASHForce].(bool); ok && v {
		return "dash", true
	}
	return "", false
}
