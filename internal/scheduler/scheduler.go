// Package scheduler is the ONE owner of Task lifecycle, queueing, chunk
// allocation and atomic commit (ADR-0002 / docs/05 §4).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asdfgh129/ud/internal/config"
	"github.com/asdfgh129/ud/internal/core"
	"github.com/asdfgh129/ud/internal/postproc"
	"github.com/asdfgh129/ud/internal/resolve"
	"github.com/asdfgh129/ud/internal/security"
	"github.com/asdfgh129/ud/internal/store"
	"github.com/asdfgh129/ud/internal/transport"
	"github.com/asdfgh129/ud/internal/version"
)

// Options wires the single scheduler to its collaborators.
type Options struct {
	Store          *store.Store
	Resolvers      *resolve.Registry
	Transports     *transport.Registry
	PostProcessors *postproc.Registry
	Guard          core.URLGuard
	Credentials    core.CredentialStore
	Config         *config.Manager
	Bus            *core.EventBus
	Logger         *slog.Logger
}

type speedCounter struct {
	mu    sync.Mutex
	bytes int64
	speed int64
	last  time.Time
}

func (c *speedCounter) add(n int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(c.last).Seconds()
	c.bytes += n
	if elapsed >= 0.5 {
		instant := int64(float64(c.bytes) / elapsed)
		if c.speed == 0 {
			c.speed = instant
		} else {
			c.speed = (c.speed*3 + instant) / 4
		}
		c.bytes, c.last = 0, now
	}
}

func (c *speedCounter) value() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.last) > 3*time.Second {
		return 0
	}
	return c.speed
}

// slots is a dynamic concurrency semaphore (maxConcurrentTasks is patchable).
type slots struct {
	mu     sync.Mutex
	used   int
	max    int
	notify chan struct{}
}

func newSlots(max int) *slots { return &slots{max: max, notify: make(chan struct{})} }

func (s *slots) setMax(n int) {
	s.mu.Lock()
	s.max = n
	close(s.notify)
	s.notify = make(chan struct{})
	s.mu.Unlock()
}

func (s *slots) acquire(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.used < s.max {
			s.used++
			s.mu.Unlock()
			return nil
		}
		ch := s.notify
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *slots) release() {
	s.mu.Lock()
	s.used--
	close(s.notify)
	s.notify = make(chan struct{})
	s.mu.Unlock()
}

// Scheduler implements core.ControlPlane.
type Scheduler struct {
	st          *store.Store
	resolvers   *resolve.Registry
	transports  *transport.Registry
	postprocs   *postproc.Registry
	guard       core.URLGuard
	creds       core.CredentialStore
	cfg         *config.Manager
	bus         *core.EventBus
	log         *slog.Logger
	limiter     *Limiter
	pathGuard   security.PathGuard
	queue       chan core.TaskID
	active      map[core.TaskID]context.CancelFunc
	inputs      map[core.TaskID]string
	speeds      map[core.TaskID]*speedCounter
	mu          sync.Mutex
	slots       *slots
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	started     time.Time
	maxAttempts int
}

func New(opts Options) (*Scheduler, error) {
	if opts.Store == nil || opts.Config == nil {
		return nil, errors.New("scheduler: store and config are required")
	}
	cfg := opts.Config.Get()
	lim := NewLimiter(cfg.Global)
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{
		st: opts.Store, resolvers: opts.Resolvers, transports: opts.Transports,
		postprocs: opts.PostProcessors, guard: opts.Guard, creds: opts.Credentials,
		cfg: opts.Config, bus: opts.Bus, log: opts.Logger, limiter: lim,
		queue: make(chan core.TaskID, 4096), active: make(map[core.TaskID]context.CancelFunc),
		inputs: make(map[core.TaskID]string), speeds: make(map[core.TaskID]*speedCounter),
		slots: newSlots(cfg.Global.MaxConcurrentTasks), ctx: ctx, cancel: cancel,
		started: time.Now(), maxAttempts: 3,
	}
	if s.bus == nil {
		s.bus = core.NewEventBus(256)
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	// Only the Limiter and SSRF Guard react to config changes. No other package
	// reads rate/security config (docs/15 §2 static check).
	opts.Config.SetOnChange(func(c config.Config) {
		lim.Apply(c.Global)
		s.slots.setMax(c.Global.MaxConcurrentTasks)
		if g, ok := s.guard.(interface{ Update(config.Config) }); ok {
			g.Update(c)
		}
	})
	// The dispatch loop is the only place that calls wg.Add(1) for workers.
	// Register it in the same WaitGroup before Close() can Wait(): a positive
	// Add must never race with Wait while the counter is zero, and Close() must
	// not return while dispatch/workers can still touch the store (docs/15 DoD).
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.dispatch()
	}()
	return s, nil
}

func (s *Scheduler) dispatch() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case id := <-s.queue:
			// A cancelled context must win even when the queue stays hot, so
			// Close() can always drain dispatch instead of spinning on queued IDs.
			if s.ctx.Err() != nil {
				return
			}
			if err := s.slots.acquire(s.ctx); err != nil {
				return
			}
			s.wg.Add(1)
			go func(id core.TaskID) {
				defer s.wg.Done()
				defer s.slots.release()
				s.process(id)
			}(id)
		}
	}
}

func (s *Scheduler) enqueue(id core.TaskID) {
	select {
	case s.queue <- id:
	default:
		go func() {
			select {
			case s.queue <- id:
			case <-s.ctx.Done():
			}
		}()
	}
}

// ---------- core.ControlPlane ----------

func (s *Scheduler) Create(ctx context.Context, in core.Input, opt core.TaskOptions) (core.TaskID, error) {
	cfg := s.cfg.Get()
	if opt.Dir == "" {
		opt.Dir = cfg.DownloadDir
	} else {
		opt.Dir = config.ExpandHome(opt.Dir)
	}
	if opt.MaxConnections <= 0 {
		opt.MaxConnections = cfg.Global.MaxConnectionsPerTask
	}
	if opt.ChunkSize <= 0 {
		if n, err := config.ParseSize(cfg.Global.ChunkSize); err == nil {
			opt.ChunkSize = n
		}
	}
	if opt.PostProcess == "" {
		opt.PostProcess = "none"
	}
	now := time.Now().UnixMilli()
	t := &core.Task{
		ID: core.NewID(), State: core.StatePending,
		Input: security.RedactURL(in.Raw), InputKind: in.Kind,
		Priority: opt.Priority, CreatedAt: now, UpdatedAt: now, Options: opt,
	}
	if err := s.st.InsertTask(ctx, t); err != nil {
		return "", err
	}
	// Keep the unredacted input in memory for this process only. After a restart
	// the redacted copy is used (documented P0 limitation, docs/06 §6).
	s.mu.Lock()
	s.inputs[t.ID] = in.Raw
	s.mu.Unlock()
	s.emit(ctx, core.Event{Type: core.EventTaskCreated, TaskID: t.ID,
		Payload: map[string]any{"state": string(t.State), "kind": string(t.InputKind)}})
	s.enqueue(t.ID)
	return t.ID, nil
}

func (s *Scheduler) Pause(ctx context.Context, id core.TaskID) error {
	t, err := s.st.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if core.IsTerminal(t.State) {
		return core.Err(core.E_ILLEGAL_TRANSITION, "task %s is terminal", id)
	}
	if cancel := s.cancelFor(id); cancel != nil {
		cancel()
	}
	if err := s.st.UpdateState(ctx, id, store.StateUpdate{To: core.StatePaused, Force: true}); err != nil {
		return err
	}
	s.emit(ctx, core.Event{Type: core.EventTaskState, TaskID: id,
		Payload: map[string]any{"state": string(core.StatePaused)}})
	return nil
}

func (s *Scheduler) Resume(ctx context.Context, id core.TaskID) error {
	t, err := s.st.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if t.State != core.StatePaused && t.State != core.StateFailed && t.State != core.StateReady {
		return core.Err(core.E_ILLEGAL_TRANSITION, "cannot resume from %s", t.State)
	}
	if err := s.st.UpdateState(ctx, id, store.StateUpdate{To: core.StateReady, Force: true}); err != nil {
		return err
	}
	// User-initiated retry: failed chunks must re-enter the pending plan.
	if _, err := s.st.ResetFailedChunks(ctx, id); err != nil {
		return err
	}
	s.emit(ctx, core.Event{Type: core.EventTaskState, TaskID: id,
		Payload: map[string]any{"state": string(core.StateReady)}})
	s.enqueue(id)
	return nil
}

func (s *Scheduler) Cancel(ctx context.Context, id core.TaskID) error {
	if _, err := s.st.GetTask(ctx, id); err != nil {
		return err
	}
	if cancel := s.cancelFor(id); cancel != nil {
		cancel()
	}
	if err := s.st.UpdateState(ctx, id, store.StateUpdate{To: core.StateCanceled, Force: true}); err != nil {
		return err
	}
	if temp, err := s.st.Option(ctx, id, "temp_path"); err == nil && temp != "" {
		_ = os.Remove(temp)
	}
	s.mu.Lock()
	delete(s.inputs, id)
	s.mu.Unlock()
	s.emit(ctx, core.Event{Type: core.EventTaskCanceled, TaskID: id,
		Payload: map[string]any{"state": string(core.StateCanceled)}})
	return nil
}

func (s *Scheduler) SetOption(ctx context.Context, id core.TaskID, opt core.TaskOptions) error {
	t, err := s.st.GetTask(ctx, id)
	if err != nil {
		return err
	}
	merged := t.Options
	if opt.Dir != "" {
		merged.Dir = config.ExpandHome(opt.Dir)
	}
	if opt.Out != "" {
		merged.Out = opt.Out
	}
	if opt.MaxConnections > 0 {
		merged.MaxConnections = opt.MaxConnections
	}
	if opt.MaxDownloadLimit != "" {
		merged.MaxDownloadLimit = opt.MaxDownloadLimit
	}
	if opt.Priority != 0 {
		merged.Priority = opt.Priority
	}
	if opt.PostProcess != "" {
		merged.PostProcess = opt.PostProcess
	}
	if opt.ChunkSize > 0 {
		merged.ChunkSize = opt.ChunkSize
	}
	if err := s.st.SaveOptions(ctx, id, merged); err != nil {
		return err
	}
	if merged.MaxDownloadLimit != "" {
		if rate, err := config.ParseRate(merged.MaxDownloadLimit); err == nil {
			s.limiter.SetTaskRate(string(id), core.BytesPerSec(rate))
		}
	}
	return nil
}

func (s *Scheduler) Get(ctx context.Context, id core.TaskID) (*core.TaskDetail, error) {
	t, err := s.st.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &core.TaskDetail{Task: *t}
	bitfield, count, verified, err := s.st.Bitfield(ctx, id)
	if err == nil {
		d.Bitfield, d.NumPieces, d.ChunksVerified = bitfield, count, verified
	}
	if count > 0 {
		chunks, _ := s.st.Chunks(ctx, id)
		if len(chunks) > 0 {
			d.PieceLength = chunks[0].Length
		}
	}
	d.Connections = t.Options.MaxConnections
	d.Speed = s.speedOf(id)
	if d.Speed > 0 && t.BytesTotal > t.BytesDone {
		d.ETA = (t.BytesTotal - t.BytesDone) / d.Speed
	}
	if t.Resource != nil {
		for _, f := range t.Resource.Files {
			if f.Selected {
				d.SelectedFiles = append(d.SelectedFiles, f.Index)
			}
		}
		for _, tier := range t.Resource.Identity.Trackers {
			d.Trackers = append(d.Trackers, tier...)
		}
	}
	return d, nil
}

func (s *Scheduler) List(ctx context.Context, f core.TaskFilter) ([]*core.Task, int, error) {
	return s.st.ListTasks(ctx, f)
}

func (s *Scheduler) Delete(ctx context.Context, id core.TaskID, withFiles bool) error {
	paths, err := s.st.DeleteTask(ctx, id)
	if err != nil {
		return err
	}
	if withFiles {
		for _, p := range paths {
			_ = os.Remove(p)
			_ = os.Remove(security.TempPath(p, s.cfg.Get().TempSuffix))
		}
	}
	s.mu.Lock()
	delete(s.inputs, id)
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) ResolveOnly(ctx context.Context, in core.Input, opt core.TaskOptions) (*core.Resource, error) {
	rc := core.ResolveContext{Ctx: ctx, Credential: opt.CredentialRef, Guard: s.guard,
		Options: map[string]any{"dir": opt.Dir, "out": opt.Out}}
	return s.resolvers.Resolve(rc, in)
}

func (s *Scheduler) Stats(ctx context.Context) (core.Stats, error) {
	total, running, completed, failed, bytes, err := s.st.TaskSummary(ctx)
	if err != nil {
		return core.Stats{}, err
	}
	var rate int64
	s.mu.Lock()
	for _, c := range s.speeds {
		rate += c.value()
	}
	s.mu.Unlock()
	return core.Stats{Tasks: total, Running: running, Completed: completed,
		Failed: failed, BytesDone: bytes, Rate: rate}, nil
}

func (s *Scheduler) Health(_ context.Context) core.Health {
	h := core.Health{
		Status: "ok", Version: version.String(), Limiter: true,
		UptimeMs: time.Since(s.started).Milliseconds(),
		Sidecars: map[string]string{}, // P0 ships zero sidecars (ADR-0008)
	}
	h.DB = s.st.Ping(context.Background()) == nil
	if !h.DB {
		h.Status = "degraded"
	}
	return h
}

func (s *Scheduler) Recover(ctx context.Context) (int, error) {
	ids, err := s.st.Recover(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		s.enqueue(id)
	}
	return len(ids), nil
}

func (s *Scheduler) Capabilities() core.Capabilities {
	// The credential backend is published, not assumed: "file" and "memory" must
	// never be mistaken for an OS keychain (ADR-0015 ④, docs/06 §9).
	backend := "memory"
	if r, ok := s.creds.(core.CredentialBackendReporter); ok && r != nil {
		backend = r.CredentialBackend()
	}
	// ADR-0031: the L1 aria2 facade is opt-in. Default (disabled) keeps the
	// contract byte-identical to the historical "L0" value.
	rpcLevel := "L0"
	if s.cfg != nil && s.cfg.Get().RPC.Enabled {
		rpcLevel = "L1"
	}
	return core.Capabilities{
		Resolvers: s.resolvers.Names(), Transports: s.transports.Names(),
		PostProcessors: s.postprocs.Names(), Sidecars: s.postprocs.Sidecars(), RPCLevel: rpcLevel,
		Credentials: backend,
	}
}

func (s *Scheduler) ConfigManager() *config.Manager    { return s.cfg }
func (s *Scheduler) Credentials() core.CredentialStore { return s.creds }

func (s *Scheduler) Close() error {
	s.cancel()
	// dispatch is registered in s.wg from New(), so Wait() covers the loop
	// itself plus every worker it spawned; no Add can follow this Wait.
	// Calling Close twice is safe (cancel is idempotent, Wait on zero returns).
	s.wg.Wait()
	return nil
}

// ---------- internal pipeline ----------

func (s *Scheduler) cancelFor(id core.TaskID) context.CancelFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id]
}

func (s *Scheduler) process(id core.TaskID) {
	s.mu.Lock()
	if _, exists := s.active[id]; exists {
		s.mu.Unlock()
		// A previous worker is winding down (Pause -> Resume). Retry shortly so
		// the resumed task is never silently dropped.
		time.AfterFunc(100*time.Millisecond, func() {
			select {
			case <-s.ctx.Done():
			default:
				s.enqueue(id)
			}
		})
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.active[id] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
		s.limiter.ForgetTask(string(id))
	}()
	if err := s.run(ctx, id); err != nil {
		s.handleFailure(ctx, id, err)
	}
}

// selectedFiles returns the task's selected files in index order (ADR-0027).
func selectedFiles(res *core.Resource) []core.ResourceFile {
	out := make([]core.ResourceFile, 0, len(res.Files))
	for _, f := range res.Files {
		if f.Selected {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// tempPathOptionKey mirrors the store's per-file temp-path option keys:
// "temp_path" for file 0, "temp_path:{i}" for additional files.
func tempPathOptionKey(fileIdx int) string {
	if fileIdx == 0 {
		return "temp_path"
	}
	return fmt.Sprintf("temp_path:%d", fileIdx)
}

// runMultiFileTask downloads a task's selected files one at a time (ADR-0027,
// docs/06 §4.4 separated-stream model): each file has its own source URL,
// chunk plan and .part path; per-file verification keeps crash-recovery
// semantics; the postprocessor (remux by default) merges every downloaded
// file into the final output, which the core commits atomically as before.
func (s *Scheduler) runMultiFileTask(ctx context.Context, id core.TaskID,
	res *core.Resource, t *core.Task, tr core.Transport) error {
	files := selectedFiles(res)
	if len(files) < 2 {
		return core.Err(core.E_INVALID_INPUT, "multi-file task needs at least two selected files")
	}
	for i, f := range files {
		if f.URL == "" {
			return core.Err(core.E_INVALID_INPUT, "file %d (%s) has no source URL", i, f.Path)
		}
	}
	// Sizes: declared by the resolver, or probed per file (HLS streams have
	// no declared size — the transport probes its own segment totals).
	sizes := make([]int64, len(files))
	for i, f := range files {
		if f.Size > 0 {
			sizes[i] = f.Size
			continue
		}
		sub := &core.Resource{ID: res.ID, Kind: res.Kind, Engine: res.Engine,
			Files: []core.ResourceFile{f},
			Mirrors: []core.Mirror{{URL: f.URL, Priority: 1}},
			Headers: res.Headers, Identity: res.Identity, Limits: res.Limits}
		p, err := tr.Probe(ctx, sub)
		if err != nil {
			return err
		}
		if p.Size <= 0 {
			return core.Err(core.E_INVALID_INPUT,
				"file %d (%s): probe returned no size; multi-file tasks need known sizes", i, f.Path)
		}
		sizes[i] = p.Size
		if err := s.st.UpdateFileSizes(ctx, res.ID, map[int]int64{i: p.Size}); err != nil {
			return err
		}
	}

	cfg := s.cfg.Get()
	chunks, err := s.st.Chunks(ctx, id)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		// First run: create per-file temp paths and chunk plans. Sizes come
		// from the resolver (yt-dlp formats carry them); no Probe — the
		// Transport probe contract describes one stream.
		var total int64
		sizeMap := make(map[int]int64, len(files))
		chunkSize := res.Limits.ChunkSize
		if chunkSize <= 0 {
			chunkSize, _ = config.ParseSize(cfg.Global.ChunkSize)
		}
		for i, f := range files {
			final, err := s.pathGuard.Clean(t.Options.Dir, f.Path)
			if err != nil {
				return err
			}
			tempPath := security.TempPath(final, cfg.TempSuffix)
			if err := s.st.SetOption(ctx, id, tempPathOptionKey(i), tempPath); err != nil {
				return err
			}
			// sizes[i] was declared by the resolver or probed above; the
			// shadowed-map mistake here produced zero-length chunks (fixed
			// 2026-11-21, caught by the multi-file integration test).
			sizeMap[i] = sizes[i]
			total += sizes[i]
			if _, err := s.st.CreateChunks(ctx, id, i, sizes[i], chunkSize); err != nil {
				return err
			}
		}
		if err := s.st.UpdateFileSizes(ctx, res.ID, sizeMap); err != nil {
			return err
		}
		if err := s.st.UpdateState(ctx, id, store.StateUpdate{
			To: core.StateReady, BytesTotal: &total, Force: true}); err != nil {
			return err
		}
	}

	if err := s.transition(ctx, id, core.StateRunning); err != nil {
		return err
	}
	if t.Options.MaxDownloadLimit != "" {
		if rate, err := config.ParseRate(t.Options.MaxDownloadLimit); err == nil {
			s.limiter.SetTaskRate(string(id), core.BytesPerSec(rate))
		}
	}
	host := hostOf(files[0].URL)
	budget, err := s.limiter.Acquire(ctx, core.Scope{
		TaskID: string(id), Host: host, Transport: tr.Name(), Direction: core.DirectionDownload,
	}, res.Limits.ChunkSize)
	if err != nil {
		return err
	}

	counter := &speedCounter{last: time.Now()}
	s.mu.Lock()
	s.speeds[id] = counter
	s.mu.Unlock()

	// Sequential per-file download: one budget (the task's rate lease), one
	// transport, a fresh file-scoped ChunkWriter per file.
	for i, f := range files {
		tempPath, err := s.st.Option(ctx, id, tempPathOptionKey(i))
		if err != nil {
			return err
		}
		if tempPath == "" {
			return core.Err(core.E_INTERNAL, "file %d has no temp path", i)
		}
		cw, err := newChunkWriterForFile(s.st, id, res.ID, tempPath, i, sizes[i],
			s.bus, s.maxAttempts, counter.add)
		if err != nil {
			return err
		}
		sub := &core.Resource{
			ID:       res.ID,
			Kind:     res.Kind,
			Engine:   res.Engine,
			Files:    []core.ResourceFile{f},
			Mirrors:  []core.Mirror{{URL: f.URL, Priority: 1, Weight: 1}},
			Headers:  res.Headers, // routing marker + site headers (ADR-0027/0028)
			Limits:   res.Limits,
			Identity: res.Identity,
		}
		s.injectHeaders(ctx, sub, t.Options)
		runErr := tr.Run(ctx, sub, cw, budget)
		syncErr := cw.Sync()
		closeErr := cw.Close()
		if runErr == nil {
			runErr = syncErr
		}
		if runErr == nil {
			runErr = closeErr
		}
		if runErr != nil {
			return runErr
		}
	}
	if err := s.checkFailedChunks(ctx, id); err != nil {
		return err
	}

	commitCtx := context.WithoutCancel(ctx)
	var bad []core.Chunk
	for i := range files {
		tempPath, err := s.st.Option(ctx, id, tempPathOptionKey(i))
		if err != nil {
			return err
		}
		fileBad, err := s.st.VerifyFileChunks(commitCtx, id, res.ID, i, tempPath)
		if err != nil {
			return err
		}
		bad = append(bad, fileBad...)
	}
	if len(bad) > 0 {
		seen := map[int64]bool{}
		for _, c := range bad {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if c.State == core.ChunkDone || c.State == core.ChunkVerified {
				_ = s.st.ResetChunk(commitCtx, c)
			} else {
				_ = s.st.Release(commitCtx, c, true)
			}
		}
		if err := s.st.UpdateState(commitCtx, id,
			store.StateUpdate{To: core.StatePaused, Force: true}); err != nil {
			return err
		}
		s.enqueue(id)
		return nil
	}

	if err := s.transition(ctx, id, core.StatePostProcessing); err != nil {
		return err
	}
	// Final merged name: Options.Out wins, else file 0's path.
	name := files[0].Path
	if t.Options.Out != "" {
		name = filepath.Base(t.Options.Out)
	}
	final, err := s.pathGuard.Clean(t.Options.Dir, name)
	if err != nil {
		return err
	}
	mergeName := t.Options.PostProcess
	if mergeName == "" || mergeName == "none" {
		// Multi-file resources exist to be merged (docs/06 §4.4); "remux" is
		// the only built-in processor that consumes several inputs.
		mergeName = "remux"
	}
	pp, err := s.postprocs.Get(mergeName)
	if err != nil {
		return err
	}
	tempPaths := make([]string, 0, len(files))
	for i := range files {
		tempPath, err := s.st.Option(ctx, id, tempPathOptionKey(i))
		if err != nil {
			return err
		}
		tempPaths = append(tempPaths, tempPath)
	}
	pres, err := pp.Process(ctx,
		core.LocalFiles{BaseDir: filepath.Dir(tempPaths[0]), Paths: tempPaths},
		core.OutputSpec{Target: final, TempSuffix: cfg.TempSuffix})
	if err != nil {
		return err
	}
	if len(pres.Outputs) == 0 {
		return core.Err(core.E_POSTPROCESS_FAILED, "postprocessor returned no output")
	}
	// The core (not the plugin) performs the atomic commit (ADR-0012).
	if err := os.Rename(pres.Outputs[0], final); err != nil {
		return core.Err(core.E_POSTPROCESS_FAILED, "atomic rename failed: %v", err)
	}
	if d, err := os.Open(filepath.Dir(final)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	if err := s.st.Commit(context.WithoutCancel(ctx), id, res.ID, 0, final); err != nil {
		return err
	}
	// The per-stream .part files are merge inputs and no longer needed.
	for _, p := range tempPaths {
		_ = os.Remove(p)
	}
	s.mu.Lock()
	delete(s.inputs, id)
	s.mu.Unlock()
	var total int64
	for i := range files {
		total += sizes[i]
	}
	s.emit(ctx, core.Event{Type: core.EventTaskCompleted, TaskID: id,
		Payload: map[string]any{"bytesTotal": total}})
	return nil
}

func (s *Scheduler) run(ctx context.Context, id core.TaskID) error {
	t, err := s.st.GetTask(ctx, id)
	if err != nil || core.IsTerminal(t.State) {
		return err
	}
	res := t.Resource

	if res == nil {
		if t.State != core.StateResolving {
			if err := s.transition(ctx, id, core.StateResolving); err != nil {
				return err
			}
		}
		raw := t.Input
		s.mu.Lock()
		if v, ok := s.inputs[id]; ok {
			raw = v
		}
		s.mu.Unlock()
		rc := core.ResolveContext{Ctx: ctx, Credential: t.Options.CredentialRef,
			Guard: s.guard, Options: map[string]any{"dir": t.Options.Dir, "out": t.Options.Out}}
		res, err = s.resolvers.Resolve(rc, core.Input{Raw: raw, Kind: t.InputKind,
			CredentialRef: t.Options.CredentialRef})
		if err != nil {
			return err
		}
		// Merge task options OVER the Resource the Resolver described, but keep
		// a manifest-declared piece length: ADR-0006 pins chunk boundaries to
		// the Metalink <pieces length>, otherwise every declared piece digest
		// would be unverifiable. A user-specified ChunkSize cannot override it.
		limits := core.TaskLimits{
			MaxConnections: t.Options.MaxConnections, ChunkSize: t.Options.ChunkSize,
			MaxDownloadLimit: t.Options.MaxDownloadLimit,
		}
		if res.Checksum != nil && len(res.Checksum.Pieces) > 0 && res.Limits.ChunkSize > 0 {
			limits.ChunkSize = res.Limits.ChunkSize
		}
		res.Limits = limits
		res.PostProcess = []core.ProcessSpec{{Type: t.Options.PostProcess}}
		// Resolver-derived headers (site Referer/UA, yt-dlp http_headers) MUST
		// survive persistence: SaveResource strips sensitive values via
		// StripSensitiveHeaders (docs/06 §6) — nil-ing here broke every
		// site-header flow after a restart (ADR-0027 fix). Never persist user
		// task-option headers raw: they carry Authorization/Cookie and are
		// merged in-memory at download time by injectHeaders.
		if err := s.st.SaveResource(ctx, id, res); err != nil {
			return err
		}
		if err := s.transition(ctx, id, core.StateReady); err != nil {
			return err
		}
	} else if err := s.transition(ctx, id, core.StateReady); err != nil {
		return err
	}

	tr, err := s.transports.Select(res)
	if err != nil {
		return err
	}
	// ADR-0028: a stream transport with a per-stream file plan (HLS master
	// carrying a separate audio rendition) expands the resource before the
	// download phase; planner failures degrade to the single-file path.
	if len(selectedFiles(res)) <= 1 {
		if fp, ok := tr.(core.FilePlanner); ok {
			if plan, perr := fp.PlanFiles(ctx, res); perr == nil && len(plan) > 1 {
				res.Files = plan
				if err := s.st.ReplaceResourceFiles(ctx, res.ID, plan); err != nil {
					return err
				}
			}
		}
	}
	// ADR-0027: a resource with more than one selected file (separated
	// audio/video streams) downloads its files one at a time — each with its
	// own source URL, chunk plan and .part path — then merges them through
	// the postprocessor (remux by default).
	if len(selectedFiles(res)) > 1 {
		return s.runMultiFileTask(ctx, id, res, t, tr)
	}
	cfg := s.cfg.Get()
	tempPath, err := s.st.Option(ctx, id, "temp_path")
	if err != nil {
		return err
	}
	chunks, err := s.st.Chunks(ctx, id)
	if err != nil {
		return err
	}
	if tempPath == "" || len(chunks) == 0 {
		// In-memory header injection before Probe; the persisted Resource stays clean.
		probeRes := shallowResource(res)
		s.injectHeaders(ctx, probeRes, t.Options)
		p, err := tr.Probe(ctx, probeRes)
		if err != nil {
			return err
		}
		name := p.Filename
		if name == "" {
			name = res.Files[0].Path
		}
		if t.Options.Out != "" {
			name = filepath.Base(t.Options.Out)
		}
		final, err := s.pathGuard.Clean(t.Options.Dir, name)
		if err != nil {
			return err
		}
		tempPath = security.TempPath(final, cfg.TempSuffix)
		if err := s.st.SetOption(ctx, id, "temp_path", tempPath); err != nil {
			return err
		}
		if err := s.st.UpdateFileSizes(ctx, res.ID, map[int]int64{0: p.Size}); err != nil {
			return err
		}
		chunkSize := res.Limits.ChunkSize
		if chunkSize <= 0 {
			chunkSize, _ = config.ParseSize(cfg.Global.ChunkSize)
		}
		if _, err := s.st.CreateChunks(ctx, id, 0, p.Size, chunkSize); err != nil {
			return err
		}
		size := p.Size
		if err := s.st.UpdateState(ctx, id, store.StateUpdate{
			To: core.StateReady, BytesTotal: &size, Force: true}); err != nil {
			return err
		}
		res.Files[0].Size = p.Size
	}

	if err := s.transition(ctx, id, core.StateRunning); err != nil {
		return err
	}
	if t.Options.MaxDownloadLimit != "" {
		if rate, err := config.ParseRate(t.Options.MaxDownloadLimit); err == nil {
			s.limiter.SetTaskRate(string(id), core.BytesPerSec(rate))
		}
	}
	host := ""
	if len(res.Mirrors) > 0 {
		host = hostOf(res.Mirrors[0].URL)
	}
	budget, err := s.limiter.Acquire(ctx, core.Scope{
		TaskID: string(id), Host: host, Transport: tr.Name(), Direction: core.DirectionDownload,
	}, res.Limits.ChunkSize)
	if err != nil {
		return err
	}
	runRes := shallowResource(res)
	s.injectHeaders(ctx, runRes, t.Options)
	runRes.Limits = res.Limits

	counter := &speedCounter{last: time.Now()}
	s.mu.Lock()
	s.speeds[id] = counter
	s.mu.Unlock()
	cw, err := newChunkWriter(s.st, id, res.ID, tempPath, res.Files[0].Size,
		s.bus, s.maxAttempts, counter.add)
	if err != nil {
		return err
	}
	err = tr.Run(ctx, runRes, cw, budget)
	syncErr := cw.Sync()
	closeErr := cw.Close()
	if err == nil {
		err = syncErr
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := s.checkFailedChunks(ctx, id); err != nil {
		return err
	}

	commitCtx := context.WithoutCancel(ctx)
	bad, err := s.st.VerifyAllChunks(commitCtx, id, res.ID, tempPath)
	if err != nil {
		return err
	}
	// ADR-0006: when a Metalink manifest declares piece digests, every chunk
	// must be verified against its declared digest with the declared algorithm.
	// Only failing pieces are released for re-download, never the whole file.
	if res.Checksum != nil && len(res.Checksum.Pieces) > 0 {
		pieceBad, err := s.st.VerifyPieceChecksums(commitCtx, id, tempPath,
			res.Checksum.Algo, res.Checksum.Pieces)
		if err != nil {
			return err
		}
		bad = append(bad, pieceBad...)
	}
	if len(bad) > 0 {
		seen := map[int64]bool{}
		for _, c := range bad {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if c.State == core.ChunkDone || c.State == core.ChunkVerified {
				_ = s.st.ResetChunk(commitCtx, c)
			} else {
				_ = s.st.Release(commitCtx, c, true)
			}
		}
		if err := s.st.UpdateState(commitCtx, id,
			store.StateUpdate{To: core.StatePaused, Force: true}); err != nil {
			return err
		}
		s.enqueue(id)
		return nil
	}
	if res.Checksum != nil && res.Checksum.Whole != "" {
		sum, err := store.WholeFileChecksumAlgo(commitCtx, tempPath, res.Checksum.Algo)
		if err != nil || !strings.EqualFold(sum, res.Checksum.Whole) {
			return core.Err(core.E_CHECKSUM_MISMATCH, "whole-file checksum mismatch")
		}
	}

	if err := s.transition(ctx, id, core.StatePostProcessing); err != nil {
		return err
	}
	final := strings.TrimSuffix(tempPath, cfg.TempSuffix)
	name := "none"
	if len(res.PostProcess) > 0 && res.PostProcess[0].Type != "" {
		name = res.PostProcess[0].Type
	}
	pp, err := s.postprocs.Get(name)
	if err != nil {
		return err
	}
	pres, err := pp.Process(ctx,
		core.LocalFiles{BaseDir: filepath.Dir(tempPath), Paths: []string{tempPath}},
		core.OutputSpec{Target: final, TempSuffix: cfg.TempSuffix})
	if err != nil {
		return err
	}
	if len(pres.Outputs) == 0 {
		return core.Err(core.E_POSTPROCESS_FAILED, "postprocessor returned no output")
	}
	// The core (not the plugin) performs the atomic commit (ADR-0012).
	if err := os.Rename(pres.Outputs[0], final); err != nil {
		return core.Err(core.E_POSTPROCESS_FAILED, "atomic rename failed: %v", err)
	}
	if d, err := os.Open(filepath.Dir(final)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	if err := s.st.Commit(context.WithoutCancel(ctx), id, res.ID, 0, final); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.inputs, id)
	s.mu.Unlock()
	s.emit(ctx, core.Event{Type: core.EventTaskCompleted, TaskID: id,
		Payload: map[string]any{"bytesTotal": res.Files[0].Size}})
	return nil
}

func (s *Scheduler) checkFailedChunks(ctx context.Context, id core.TaskID) error {
	chunks, err := s.st.Chunks(ctx, id)
	if err != nil {
		return err
	}
	for _, c := range chunks {
		if c.State == core.ChunkFailed {
			return core.Err(core.E_TRANSIENT_NETWORK, "chunk %d failed permanently", c.Index)
		}
	}
	return nil
}

func (s *Scheduler) handleFailure(ctx context.Context, id core.TaskID, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Pause/Cancel/Shutdown already wrote the authoritative state.
		return
	}
	e := core.AsError(err)
	dbCtx := context.WithoutCancel(ctx)
	// A concurrent Cancel / Delete may land between the step that failed and this
	// handler, e.g. run() reads the task, then Cancel writes StateCanceled, then the
	// Resolving transition fails with E_ILLEGAL_TRANSITION. Completed / Canceled are
	// terminal, but the write below needs Force for legitimate Paused / Failed
	// retries, so the guard has to be explicit: without it a user-cancelled task
	// would be resurrected as Failed carrying "illegal state transition" text.
	// Regression: TestHandleFailureKeepsConcurrentCanceledState.
	if cur, gerr := s.st.GetTask(dbCtx, id); gerr == nil && core.IsTerminal(cur.State) {
		return
	}
	_ = s.st.UpdateState(dbCtx, id, store.StateUpdate{
		To: core.StateFailed, ErrorKind: e.Kind.String(),
		ErrorMessage: security.Redact(e.Message), Force: true,
	})
	s.emit(dbCtx, core.Event{Type: core.EventTaskFailed, TaskID: id,
		Payload: map[string]any{"errorCode": e.Code, "message": security.Redact(e.Message)}})
	s.log.Warn("task failed", "taskId", string(id), "code", e.Code, "error", security.Redact(e.Message))
}

func (s *Scheduler) transition(ctx context.Context, id core.TaskID, to core.TaskState) error {
	current, err := s.st.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if current.State == to {
		return nil
	}
	force := current.State == core.StatePaused || current.State == core.StateFailed
	if err := s.st.UpdateState(ctx, id, store.StateUpdate{To: to, Force: force}); err != nil {
		return err
	}
	s.emit(ctx, core.Event{Type: core.EventTaskState, TaskID: id,
		Payload: map[string]any{"state": string(to)}})
	return nil
}

func (s *Scheduler) emit(ctx context.Context, e core.Event) {
	s.bus.Publish(e)
	_ = s.st.InsertEvent(context.WithoutCancel(ctx), e)
}

func (s *Scheduler) speedOf(id core.TaskID) int64 {
	s.mu.Lock()
	c := s.speeds[id]
	s.mu.Unlock()
	if c == nil {
		return 0
	}
	return c.value()
}

// injectHeaders merges the headers a Resource needs for its download:
// resolver-derived site headers (yt-dlp http_headers, lux Referer — required
// by the CDN, stripped of secrets at persistence) first, then the task's own
// options (user intent wins), then the credential secret. A full replace
// here silently broke every site-header flow (ADR-0027 fix).
func (s *Scheduler) injectHeaders(ctx context.Context, r *core.Resource, opt core.TaskOptions) {
	merged := make(map[string]string, len(r.Headers)+len(opt.Headers)+1)
	for k, v := range r.Headers {
		merged[k] = v
	}
	for k, v := range opt.Headers {
		merged[k] = v
	}
	if opt.CredentialRef != "" && s.creds != nil {
		if secret, err := s.creds.Get(ctx, opt.CredentialRef); err == nil && secret != "" {
			merged["Authorization"] = secret
		}
	}
	r.Headers = merged
}

func shallowResource(r *core.Resource) *core.Resource {
	cp := *r
	cp.Files = append([]core.ResourceFile(nil), r.Files...)
	cp.Mirrors = append([]core.Mirror(nil), r.Mirrors...)
	cp.PostProcess = append([]core.ProcessSpec(nil), r.PostProcess...)
	return &cp
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
