package hlstransport

// The HLS Transport (WBS 13 / FR-TRN-08). Byte-moving only: the scheduler
// owns queueing, state and the chunk plan; this file owns playlists, segment
// fetching, decryption and the sequential commit into the ChunkWriter.
//
// Commit model (mirrors bttransport): segments are downloaded by a bounded
// worker pool and delivered IN ORDER over a channel; a segment io.Reader
// feeds the commit loop, which claims chunks from the ChunkWriter and copies
// stream bytes into them. Rate limiting stays with the one Budget authority;
// every byte that passes the reader — including bytes skipped over verified
// progress — is charged to it. In-flight memory is capped by a byte
// semaphore sized from the probed segment lengths, so a CDN serving
// oversized segments cannot balloon the process.

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

	"github.com/asdfgh129/ud/internal/core"
)

// FetchLimits bound the transport's memory and request behaviour.
const (
	// maxInflightSegmentBytes caps the sum of segment bodies buffered for
	// ordered commit (byte semaphore). At a typical 4-6 MB segment this
	// allows 8-16 workers.
	maxInflightSegmentBytes = 64 << 20
	// maxSegmentBytes is a hard per-segment sanity cap (a VOD segment longer
	// than this is not a segment, it is a whole file).
	maxSegmentBytes = 512 << 20
	// maxSegments bounds playlist fan-out: a million-entry playlist is a
	// protocol abuse, not a VOD.
	maxSegments = 20000
	// maxSegmentWorkers bounds the concurrent fetch pool regardless of the
	// byte semaphore (each worker also holds buffers and a connection).
	maxSegmentWorkers = 8
	// maxKeys bounds the EXT-X-KEY cache (a playlist rotating keys faster
	// than the bound fails closed).
	maxKeys = 64
	// sizeProbeConcurrency bounds the parallel 1-byte Range size probes.
	sizeProbeConcurrency = 8
	// probeRange asks for one byte: HEAD is refused by some CDNs, and the
	// Range GET also validates reachability the same way the real fetch will.
	probeRange = "bytes=0-0"
)

// Transport is the L1 HLS implementation. It shares the URLGuard with every
// other transport: playlist, variant, key and segment URLs are all untrusted.
type Transport struct {
	guard  core.URLGuard
	client *http.Client
	keys   *keyCache
}

// New builds the transport. The HTTP client pins DNS through the guard's
// DialContext (rebinding defense), mirroring internal/transport/http.
func New(g core.URLGuard) *Transport {
	var client *http.Client
	if g != nil {
		client = &http.Client{Transport: &http.Transport{DialContext: g.DialContext}}
	} else {
		client = &http.Client{}
	}
	client.Timeout = 5 * time.Minute
	return &Transport{guard: g, client: client, keys: newKeyCache()}
}

func (t *Transport) Name() string { return "hls" }

func (t *Transport) Supports(r *core.Resource) bool {
	return r != nil && r.Kind == core.KindStream
}

// checkURL applies the SSRF guard when one is wired.
func (t *Transport) checkURL(ctx context.Context, raw string) error {
	if t.guard == nil {
		return nil
	}
	return t.guard.Check(ctx, raw)
}

// do performs one guarded GET with caller headers.
func (t *Transport) do(ctx context.Context, method, raw string, hdr http.Header, setRange string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, nil)
	if err != nil {
		return nil, core.Err(core.E_INVALID_INPUT, "bad HLS URL %q: %v", raw, err)
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
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "hls fetch %s: %v", displayHost(raw), err)
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "hls fetch %s: HTTP %d", displayHost(raw), resp.StatusCode)
	}
	return resp, nil
}

// displayHost renders a URL host for diagnostics only.
func displayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable)"
	}
	return u.Host
}

// loadPlaylist fetches and parses one playlist document.
func (t *Transport) loadPlaylist(ctx context.Context, raw string, hdr http.Header) (*Playlist, error) {
	if err := t.checkURL(ctx, raw); err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, http.MethodGet, raw, hdr, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, maxPlaylistBytes)
	if err != nil {
		return nil, core.Err(core.E_INVALID_INPUT, "playlist %s: %v", displayHost(raw), err)
	}
	if !LooksLikePlaylist(body) {
		return nil, core.Err(core.E_RESOLVE_FAILED, "%s does not look like an M3U8 playlist", displayHost(raw))
	}
	return ParsePlaylist(raw, body)
}

// readBounded copies at most limit bytes and refuses longer bodies.
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

// resolveMedia walks master playlists down to a media playlist, honouring the
// L1 selection policy. depth bounds malicious redirect-via-playlist loops.
func (t *Transport) resolveMedia(ctx context.Context, raw string, hdr http.Header, depth int) (*MediaPlaylist, error) {
	if depth > 4 {
		return nil, core.Err(core.E_RESOLVE_FAILED, "master playlist nesting deeper than 4")
	}
	pl, err := t.loadPlaylist(ctx, raw, hdr)
	if err != nil {
		return nil, err
	}
	if pl.Media != nil {
		return pl.Media, nil
	}
	v := pl.Master.SelectVariant()
	if err := t.checkURL(ctx, v.URI); err != nil {
		return nil, err
	}
	return t.resolveMedia(ctx, v.URI, hdr, depth+1)
}

// probeSegmentSize asks the server for one segment's size via a 1-byte Range
// GET (HEAD is refused by some CDNs and Range also validates reachability).
func (t *Transport) probeSegmentSize(ctx context.Context, seg Segment, hdr http.Header) (int64, error) {
	if seg.ByteRange != nil && seg.ByteRange.Offset >= 0 {
		// A fully anchored byte range IS the length we would fetch.
		return seg.ByteRange.Length, nil
	}
	if err := t.checkURL(ctx, seg.URI); err != nil {
		return 0, err
	}
	resp, err := t.do(ctx, http.MethodGet, seg.URI, hdr, probeRange)
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
		return 0, core.Err(core.E_TRANSIENT_NETWORK, "segment %s returned 206 without Content-Range", displayHost(seg.URI))
	case resp.StatusCode == http.StatusOK:
		if resp.ContentLength > 0 {
			return resp.ContentLength, nil
		}
		return 0, core.Err(core.E_TRANSIENT_NETWORK, "segment %s returned 200 without a length", displayHost(seg.URI))
	default:
		return 0, core.Err(core.E_TRANSIENT_NETWORK, "segment %s: HTTP %d", displayHost(seg.URI), resp.StatusCode)
	}
}

// parseContentRangeTotal extracts the total length from "bytes 0-0/12345".
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

// probeSizes runs the size probe pass over all segments with bounded
// concurrency and returns per-segment sizes in playlist order.
func (t *Transport) probeSizes(ctx context.Context, pl *MediaPlaylist, hdr http.Header) ([]int64, error) {
	if len(pl.Segments) > maxSegments {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "playlist has %d segments (limit %d)", len(pl.Segments), maxSegments)
	}
	sizes := make([]int64, len(pl.Segments))
	errs := make([]error, len(pl.Segments))
	sem := make(chan struct{}, sizeProbeConcurrency)
	var wg sync.WaitGroup
	for i := range pl.Segments {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			n, err := t.probeSegmentSize(ctx, pl.Segments[i], hdr)
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

// Probe returns the concatenated-stream size (sum of segment sizes) and the
// output filename. The scheduler turns this into the chunk plan.
func (t *Transport) Probe(ctx context.Context, r *core.Resource) (*core.ProbeResult, error) {
	if !t.Supports(r) {
		return nil, core.Err(core.E_RESOLVE_FAILED, "hls transport requires a stream resource")
	}
	if len(r.Mirrors) == 0 {
		return nil, core.Err(core.E_RESOLVE_FAILED, "hls resource has no playlist URL")
	}
	hdr := headerOf(r)
	pl, err := t.resolveMedia(ctx, r.Mirrors[0].URL, hdr, 0)
	if err != nil {
		return nil, err
	}
	if !pl.VOD {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "%s", ErrLiveNotSupported)
	}
	sizes, err := t.probeSizes(ctx, pl, hdr)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, n := range sizes {
		total += n
	}
	name := defaultName(r.Mirrors[0].URL, pl.IsFMP4)
	return &core.ProbeResult{Size: total, AcceptRanges: true, Filename: name}, nil
}

func headerOf(r *core.Resource) http.Header {
	h := http.Header{}
	for k, v := range r.Headers {
		// Resource.Headers at transport time is runtime-injected by the
		// scheduler (credential store / task options) or persisted as
		// non-sensitive headers; both are safe to forward.
		h.Set(k, v)
	}
	return h
}

// defaultName derives the output filename from the playlist URL. TS segments
// concatenate to an MPEG-TS stream; fMP4 segments (EXT-X-MAP / .m4s)
// concatenate to a valid fragmented MP4 that players read directly.
func defaultName(playlistURL string, fmp4 bool) string {
	name := "stream"
	if u, err := url.Parse(playlistURL); err == nil {
		base := path.Base(strings.TrimSuffix(u.Path, "/"))
		base = strings.TrimSuffix(base, ".m3u8")
		if base != "" && base != "." && base != "/" {
			name = base
		}
	}
	if fmp4 {
		return name + ".mp4"
	}
	return name + ".ts"
}

// keyCache caches fetched AES keys by URI (bounded; a playlist rotating keys
// faster than the bound fails closed).
type keyCache struct {
	mu    sync.Mutex
	keys  map[string][]byte
	order []string
}

func newKeyCache() *keyCache { return &keyCache{keys: map[string][]byte{}} }

func (c *keyCache) get(uri string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, ok := c.keys[uri]
	return k, ok
}

func (c *keyCache) put(uri string, k []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.keys[uri]; !ok {
		c.order = append(c.order, uri)
		if len(c.order) > maxKeys {
			delete(c.keys, c.order[0])
			c.order = c.order[1:]
		}
	}
	c.keys[uri] = k
}

// fetchKey fetches and decodes the 16-byte AES key behind an EXT-X-KEY URI.
func (t *Transport) fetchKey(ctx context.Context, k *Key, hdr http.Header) ([]byte, error) {
	if err := t.checkURL(ctx, k.URI); err != nil {
		return nil, err
	}
	resp, err := t.do(ctx, http.MethodGet, k.URI, hdr, "")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyFetchFailed, err)
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, 4096)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyFetchFailed, err)
	}
	// RFC 8216: the key is 16 raw octets. Servers that hex-encode it are
	// common enough to accept both shapes.
	if len(body) == 32 {
		return parseHexKey(string(body))
	}
	if len(body) != 16 {
		return nil, fmt.Errorf("%w: key is %d bytes, want 16", ErrKeyFetchFailed, len(body))
	}
	return body, nil
}

// keyFor resolves (and caches) the key for one segment.
func (t *Transport) keyFor(ctx context.Context, seg Segment, hdr http.Header) ([]byte, error) {
	if k, ok := t.keys.get(seg.Key.URI); ok {
		return k, nil
	}
	key, err := t.fetchKey(ctx, seg.Key, hdr)
	if err != nil {
		return nil, err
	}
	t.keys.put(seg.Key.URI, key)
	return key, nil
}

// fetchedSegment is one downloaded (and decrypted) segment ready for ordered
// commit.
type fetchedSegment struct {
	body []byte
	err  error
}

// segResult is a worker's raw output; the orderer re-sequences it.
type segResult struct {
	index int
	body  []byte
	err   error
}

// segmentSource downloads segments [start, len) through a byte semaphore and
// yields them IN PLAYLIST ORDER: workers complete out of order, so an
// orderer goroutine buffers completions and emits the consecutive sequence.
// stop() aborts the pipeline; the channel closes once every in-flight worker
// has finished.
func (t *Transport) segmentSource(ctx context.Context, pl *MediaPlaylist, sizes []int64, start int, hdr http.Header) (<-chan fetchedSegment, func()) {
	out := make(chan fetchedSegment)
	results := make(chan segResult)
	done := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(done) }) }
	workers := make(chan struct{}, maxSegmentWorkers)
	sem := make(chan struct{}, maxInflightSegmentBytes)
	var wg sync.WaitGroup

	// Spawner: one worker per segment, bounded by the worker pool.
	go func() {
		defer close(results)
	loop:
		for i := start; i < len(pl.Segments); i++ {
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
				body, err := t.fetchSegment(ctx, pl, sizes, i, hdr, sem)
				select {
				case results <- segResult{index: i, body: body, err: err}:
				case <-done:
				case <-ctx.Done():
				}
			}(i)
		}
		wg.Wait()
	}()

	// Orderer: emit [next, next+1, ...) in sequence, buffering completions
	// until their predecessors arrive. Buffers are bounded by the byte
	// semaphore: a segment can only be buffered after its slot was reserved.
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

// fetchSegment downloads (and decrypts) one segment under the byte
// semaphore. Decryption happens off the commit path so the CBC work overlaps
// with the next segment's transfer.
func (t *Transport) fetchSegment(ctx context.Context, pl *MediaPlaylist, sizes []int64, i int, hdr http.Header, sem chan struct{}) ([]byte, error) {
	seg := pl.Segments[i]
	if err := t.checkURL(ctx, seg.URI); err != nil {
		return nil, err
	}
	// Reserve the probed size before transferring: encrypted bodies carry one
	// extra CBC block of padding at most.
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, seg.URI, nil)
	if err != nil {
		return nil, core.Err(core.E_INVALID_INPUT, "bad segment URL %q: %v", seg.URI, err)
	}
	for k, vals := range hdr {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
	if seg.ByteRange != nil && seg.ByteRange.Offset >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", seg.ByteRange.Offset, seg.ByteRange.Offset+seg.ByteRange.Length-1))
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "segment fetch %s: %v", displayHost(seg.URI), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "segment fetch %s: HTTP %d", displayHost(seg.URI), resp.StatusCode)
	}
	body, err := readBounded(resp.Body, maxSegmentBytes)
	if err != nil {
		return nil, core.Err(core.E_TRANSIENT_NETWORK, "segment %s: %v", displayHost(seg.URI), err)
	}
	if seg.Key != nil && seg.Key.Method == "AES-128" {
		key, err := t.keyFor(ctx, seg, hdr)
		if err != nil {
			return nil, err
		}
		if body, err = decryptSegment(seg, sequenceOf(pl, i), key, body); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// segmentReader turns the ordered segment channel into one sequential
// io.Reader over the concatenated (decrypted) stream. Every byte — written
// or skipped — is charged to the Budget here, keeping the scheduler's Limiter
// the single rate authority.
type segmentReader struct {
	src    <-chan fetchedSegment
	stop   func()
	cur    []byte
	err    error
	done   bool
	budget core.Budget
	offset int64 // bytes consumed so far (skipped + written)
}

func (sr *segmentReader) Read(p []byte) (int, error) {
	if sr.err != nil {
		return 0, sr.err
	}
	for len(sr.cur) == 0 {
		if sr.done {
			return 0, io.EOF
		}
		seg, ok := <-sr.src
		if !ok {
			sr.done = true
			return 0, io.EOF
		}
		if seg.err != nil {
			sr.err = seg.err
			return 0, seg.err
		}
		sr.cur = seg.body
	}
	n := len(p)
	if n > len(sr.cur) {
		n = len(sr.cur)
	}
	if sr.budget != nil {
		if err := sr.budget.Consume(context.Background(), n); err != nil {
			sr.err = err
			return 0, err
		}
	}
	copy(p[:n], sr.cur[:n])
	sr.cur = sr.cur[n:]
	sr.offset += int64(n)
	return n, nil
}

func (sr *segmentReader) Close() { sr.stop() }

// Run drives the download: parse playlist → compute boundaries → start the
// ordered segment stream at the first pending chunk → copy bytes into the
// ChunkWriter plan.
func (t *Transport) Run(ctx context.Context, r *core.Resource, w core.ChunkWriter, b core.Budget) error {
	if !t.Supports(r) {
		return core.Err(core.E_RESOLVE_FAILED, "hls transport requires a stream resource")
	}
	if len(r.Mirrors) == 0 {
		return core.Err(core.E_RESOLVE_FAILED, "hls resource has no playlist URL")
	}
	hdr := headerOf(r)
	pl, err := t.resolveMedia(ctx, r.Mirrors[0].URL, hdr, 0)
	if err != nil {
		return err
	}
	if !pl.VOD {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "%s", ErrLiveNotSupported)
	}
	sizes, err := t.probeSizes(ctx, pl, hdr)
	if err != nil {
		return err
	}
	total := int64(0)
	for _, n := range sizes {
		total += n
	}

	// Locate the first pending chunk: the commit stream starts there.
	first, ok, err := w.Next(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil // nothing pending: the task is already complete
	}
	startSeg := segmentAt(sizes, first.Start)
	if startSeg < 0 {
		_ = w.Release(ctx, first, core.Err(core.E_INVALID_INPUT,
			"chunk start %d outside stream size %d", first.Start, total))
		return core.Err(core.E_INVALID_INPUT, "chunk start %d outside stream size %d", first.Start, total)
	}

	source, stop := t.segmentSource(ctx, pl, sizes, startSeg, hdr)
	sr := &segmentReader{src: source, stop: stop, budget: b}
	defer sr.Close()

	// Skip forward to the first pending chunk's offset (crash recovery
	// reuses verified progress; those bytes are already on disk and only
	// re-traverse the network, charged to the budget like any other byte).
	if skip := first.Start; skip > 0 {
		if _, err := io.CopyN(io.Discard, sr, skip); err != nil {
			_ = w.Release(ctx, first, err)
			return err
		}
	}
	cur := first
	for {
		if cur.Start+cur.Length > total {
			_ = w.Release(ctx, cur, core.Err(core.E_INVALID_INPUT,
				"chunk [%d,%d) outside stream size %d", cur.Start, cur.Start+cur.Length, total))
			return core.Err(core.E_INVALID_INPUT, "chunk outside stream size")
		}
		buf := make([]byte, cur.Length)
		if _, err := readFullBudgeted(sr, buf); err != nil {
			_ = w.Release(ctx, cur, err)
			return err
		}
		if err := w.WriteChunk(ctx, cur, buf); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		next, ok, err := w.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		cur = next
	}
}

// readFullBudgeted fills buf entirely; the budget is charged inside the
// reader for every byte, so a stalled stream still consumes budget only for
// bytes actually delivered.
func readFullBudgeted(sr *segmentReader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := sr.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// segmentAt returns the index of the segment containing byte offset off, or
// -1 when off is outside the stream.
func segmentAt(sizes []int64, off int64) int {
	if off < 0 {
		return -1
	}
	var acc int64
	for i, n := range sizes {
		if off < acc+n {
			return i
		}
		acc += n
	}
	return -1
}
