package dashtransport

// The DASH Transport (WBS 13 second half, FR-TRN-08). Byte-moving only: the
// scheduler owns queueing, state and the chunk plan; this file owns MPD
// fetching, representation selection, segment fetching and the sequential
// commit into the ChunkWriter (shared with hlstransport via
// tputil.SequentialCommit / tputil.NewChanReader — one first-chunk / bounds /
// release semantics for every sequential transport).
//
// The commit stream is [init segment][segment 0][segment 1]...: fMP4 init +
// media segments concatenate to a playable fragmented MP4 (ADR-0024).
// Resume maps the first pending chunk offset onto that stream, exactly like
// the HLS transport.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/transport/tputil"
)

// Fetch limits: same posture as the HLS transport (ADR-0023).
const (
	maxInflightSegmentBytes = 64 << 20
	maxSegmentBytes         = 512 << 20
	maxSegmentWorkers       = 8
	sizeProbeConcurrency    = 8
	probeRange              = "bytes=0-0"
)

// Transport is the L1 DASH implementation. The URLGuard covers the MPD, the
// BaseURL chain, init and segment URLs.
type Transport struct {
	guard  core.URLGuard
	client *http.Client
}

// New builds the transport with DNS pinned through the guard (rebinding
// defense), mirroring internal/transport/http.
func New(g core.URLGuard) *Transport {
	var client *http.Client
	if g != nil {
		client = &http.Client{Transport: &http.Transport{DialContext: g.DialContext}}
	} else {
		client = &http.Client{}
	}
	client.Timeout = 5 * time.Minute
	return &Transport{guard: g, client: client}
}

func (t *Transport) Name() string { return "dash" }

// Supports claims stream resources carrying the DASH marker or a .mpd URL
// (ADR-0024: the two stream transports partition the kind).
func (t *Transport) Supports(r *core.Resource) bool {
	if r == nil || r.Kind != core.KindStream {
		return false
	}
	if v := r.Headers[core.HeaderStreamKind]; v != "" {
		return v == core.StreamKindDASH
	}
	if len(r.Mirrors) == 0 {
		return false
	}
	u, err := url.Parse(r.Mirrors[0].URL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".mpd")
}

func (t *Transport) checkURL(ctx context.Context, raw string) error {
	if t.guard == nil {
		return nil
	}
	return t.guard.Check(ctx, raw)
}

func (t *Transport) do(ctx context.Context, raw string, hdr http.Header, setRange string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, core.Err(core.E_INVALID_INPUT, "bad DASH URL %q: %v", raw, err)
	}
	for k, vals := range hdr {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	if setRange != "" {
		req.Header.Set("Range", setRange)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "dash fetch %s: %v", displayHost(raw), err)
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "dash fetch %s: HTTP %d", displayHost(raw), resp.StatusCode)
	}
	return resp, nil
}

func displayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable)"
	}
	return u.Host
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("body exceeds %d bytes", limit)
	}
	return body, nil
}

func headerOf(r *core.Resource) http.Header {
	h := http.Header{}
	for k, v := range r.Headers {
		// The internal stream-kind routing marker stays local (ADR-0024).
		if k == core.HeaderStreamKind {
			continue
		}
		h.Set(k, v)
	}
	return h
}

// loadMPD fetches and parses the MPD document.
func (t *Transport) loadMPD(ctx context.Context, raw string, hdr http.Header) (*MPD, error) {
	if err := t.checkURL(ctx, raw); err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, raw, hdr, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, maxMPDBytes)
	if err != nil {
		return nil, core.Err(core.E_INVALID_INPUT, "mpd %s: %v", displayHost(raw), err)
	}
	if !LooksLikeMPD(body) {
		return nil, core.Err(core.E_RESOLVE_FAILED, "%s does not look like an MPD document", displayHost(raw))
	}
	return ParseMPD(raw, body)
}

// selected resolves the MPD to its single selected representation.
func (t *Transport) selected(ctx context.Context, mpdURL string, hdr http.Header) (*MPD, *Representation, error) {
	m, err := t.loadMPD(ctx, mpdURL, hdr)
	if err != nil {
		return nil, nil, err
	}
	return m, m.SelectRepresentation(), nil
}

// probeSize asks one URL's length via a 1-byte Range GET (HEAD is refused by
// some CDNs; the Range GET also validates reachability like the real fetch).
func (t *Transport) probeSize(ctx context.Context, raw string, hdr http.Header) (int64, error) {
	if err := t.checkURL(ctx, raw); err != nil {
		return 0, err
	}
	resp, err := t.do(ctx, raw, hdr, probeRange)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if n, ok := parseContentRangeTotal(cr); ok {
				return n, nil
			}
		}
		return 0, core.Err(core.E_TRANSIENT_NETWORK, "segment %s returned 206 without Content-Range", displayHost(raw))
	case resp.StatusCode == http.StatusOK:
		if resp.ContentLength > 0 {
			return resp.ContentLength, nil
		}
		return 0, core.Err(core.E_TRANSIENT_NETWORK, "segment %s returned 200 without a length", displayHost(raw))
	default:
		return 0, core.Err(core.E_TRANSIENT_NETWORK, "segment %s: HTTP %d", displayHost(raw), resp.StatusCode)
	}
}

func parseContentRangeTotal(cr string) (int64, bool) {
	i := strings.IndexByte(cr, '/')
	if i < 0 {
		return 0, false
	}
	total := cr[i+1:]
	if total == "*" {
		return 0, false
	}
	var n int64
	for _, c := range total {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// probeSizes bounds the fan-out and probes init + segments concurrently.
func (t *Transport) probeSizes(ctx context.Context, urls []string, hdr http.Header) ([]int64, error) {
	if len(urls) > maxSegments {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "%d segments exceed the limit %d", len(urls), maxSegments)
	}
	sizes := make([]int64, len(urls))
	errs := make([]error, len(urls))
	sem := make(chan struct{}, sizeProbeConcurrency)
	var wg sync.WaitGroup
	for i := range urls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n, err := t.probeSize(ctx, urls[i], hdr)
			sizes[i], errs[i] = n, err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return sizes, nil
}

// Probe returns the concatenated-stream size (init + segments) and filename.
func (t *Transport) Probe(ctx context.Context, r *core.Resource) (*core.ProbeResult, error) {
	if !t.Supports(r) {
		return nil, core.Err(core.E_RESOLVE_FAILED, "dash transport requires a stream resource")
	}
	if len(r.Mirrors) == 0 {
		return nil, core.Err(core.E_RESOLVE_FAILED, "dash resource has no MPD URL")
	}
	hdr := headerOf(r)
	m, rep, err := t.selected(ctx, r.Mirrors[0].URL, hdr)
	if err != nil {
		return nil, err
	}
	initURL, segURLs, err := m.ExpandSegmentURLs(r.Mirrors[0].URL, rep)
	if err != nil {
		return nil, err
	}
	urls := segURLs
	if initURL != "" {
		urls = append([]string{initURL}, segURLs...)
	}
	sizes, err := t.probeSizes(ctx, urls, hdr)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, n := range sizes {
		total += n
	}
	return &core.ProbeResult{Size: total, AcceptRanges: true, Filename: defaultName(r.Mirrors[0].URL)}, nil
}

// defaultName derives the output filename from the MPD URL. DASH fMP4
// segments concatenate to a fragmented MP4 (ADR-0024).
func defaultName(mpdURL string) string {
	name := "stream"
	if u, err := url.Parse(mpdURL); err == nil {
		base := path.Base(strings.TrimSuffix(u.Path, "/"))
		base = strings.TrimSuffix(base, ".mpd")
		if base != "" && base != "." && base != "/" {
			name = base
		}
	}
	return name + ".mp4"
}

// fetchedSegment is one downloaded segment ready for ordered commit.
type fetchedSegment struct {
	body []byte
	err  error
}

// segmentSource downloads [initURL + segments[start:]] with a byte semaphore
// and yields them in order (worker pool + orderer, same shape as HLS).
func (t *Transport) segmentSource(ctx context.Context, urls []string, sizes []int64, start int, hdr http.Header) (<-chan fetchedSegment, func()) {
	out := make(chan fetchedSegment)
	results := make(chan segResult)
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(done) }) }
	workers := make(chan struct{}, maxSegmentWorkers)
	sem := make(chan struct{}, maxInflightSegmentBytes)
	var wg sync.WaitGroup
	go func() {
		defer close(results)
	loop:
		for i := start; i < len(urls); i++ {
			select {
			case <-ctx.Done():
				break loop
			case <-done:
				break loop
			case workers <- struct{}{}:
			}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				defer func() { <-workers }()
				body, err := t.fetchSegment(ctx, urls[i], sizes[i], hdr, sem)
				select {
				case results <- segResult{index: i, body: body, err: err}:
				case <-done:
				case <-ctx.Done():
				}
			}(i)
		}
		wg.Wait()
	}()
	go func() {
		defer close(out)
		bodies := make(map[int][]byte)
		errs := make(map[int]error)
		next := start
		emit := func(body []byte, err error) bool {
			select {
			case out <- fetchedSegment{body: body, err: err}:
				return true
			case <-done:
				return false
			case <-ctx.Done():
				return false
			}
		}
		for r := range results {
			if r.err != nil {
				errs[r.index] = r.err
			} else {
				bodies[r.index] = r.body
			}
			for {
				if body, ok := bodies[next]; ok {
					delete(bodies, next)
					if !emit(body, nil) {
						return
					}
					next++
					continue
				}
				if e, ok := errs[next]; ok {
					delete(errs, next)
					if !emit(nil, e) {
						return
					}
					next++
					continue
				}
				break
			}
		}
	}()
	return out, stop
}

// segResult is a worker's raw output; the orderer re-sequences it.
type segResult struct {
	index int
	body  []byte
	err   error
}

// fetchSegment downloads one segment under the byte semaphore.
func (t *Transport) fetchSegment(ctx context.Context, raw string, size int64, hdr http.Header, sem chan struct{}) ([]byte, error) {
	if err := t.checkURL(ctx, raw); err != nil {
		return nil, err
	}
	reserve := size
	if reserve <= 0 {
		reserve = 1 // unknown size still occupies one slot
	}
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	resp, err := t.do(ctx, raw, hdr, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, maxSegmentBytes)
	if err != nil {
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "segment %s: %v", displayHost(raw), err)
	}
	return body, nil
}

// Run drives the download: fetch MPD → select representation → expand
// segments → start the ordered stream (init first) at the first pending
// chunk → commit via the shared sequential loop.
func (t *Transport) Run(ctx context.Context, r *core.Resource, w core.ChunkWriter, b core.Budget) error {
	if !t.Supports(r) {
		return core.Err(core.E_RESOLVE_FAILED, "dash transport requires a stream resource")
	}
	if len(r.Mirrors) == 0 {
		return core.Err(core.E_RESOLVE_FAILED, "dash resource has no MPD URL")
	}
	hdr := headerOf(r)
	m, rep, err := t.selected(ctx, r.Mirrors[0].URL, hdr)
	if err != nil {
		return err
	}
	initURL, segURLs, err := m.ExpandSegmentURLs(r.Mirrors[0].URL, rep)
	if err != nil {
		return err
	}
	urls := segURLs
	if initURL != "" {
		urls = append([]string{initURL}, segURLs...)
	}
	sizes, err := t.probeSizes(ctx, urls, hdr)
	if err != nil {
		return err
	}
	total := int64(0)
	for _, n := range sizes {
		total += n
	}

	first, ok, err := w.Next(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil // nothing pending
	}
	startURL := 0
	streamStart := int64(0)
	{
		var acc int64
		found := false
		for i, n := range sizes {
			if first.Start < acc+n {
				startURL, streamStart, found = i, acc, true
				break
			}
			acc += n
		}
		if !found {
			_ = w.Release(ctx, first, core.Err(core.E_INVALID_INPUT,
				"chunk start %d outside stream size %d", first.Start, total))
			return core.Err(core.E_INVALID_INPUT, "chunk start %d outside stream size %d", first.Start, total)
		}
	}

	source, stop := t.segmentSource(ctx, urls, sizes, startURL, hdr)
	sr := tputil.NewChanReader(func() ([]byte, error, bool) {
		seg, ok := <-source
		if !ok {
			return nil, nil, false
		}
		return seg.body, seg.err, true
	}, stop, b)
	defer sr.Close()

	// Every byte — skipped or written — is charged to the Budget inside the
	// reader; the shared loop owns the first-chunk / bounds / release rules.
	return tputil.SequentialCommit(ctx, sr, total, streamStart, w, first)
}
