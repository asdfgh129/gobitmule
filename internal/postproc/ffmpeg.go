// Package postproc's optional Transcode processor executes a user-provided
// ffmpeg CLI on ALREADY DOWNLOADED local files (ADR-0011 / ADR-0012). It never
// accepts a URL, never creates the final target, and never enters the transport
// hot path. Missing/unsuitable binaries degrade to E_CAPABILITY_UNAVAILABLE so
// the zero-sidecar download path keeps working.
package postproc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asdfgh129/ud/internal/core"
)

// TranscodeName is the PostProcessor type accepted by TaskOptions.PostProcess.
const TranscodeName = "ffmpeg-transcode"

const (
	defaultFFmpegBinary = "ffmpeg"
	defaultThreads      = 2
	defaultConcurrent   = 1
	defaultTimeout      = 30 * time.Minute
	probeTimeout        = 10 * time.Second
	stderrTailBytes     = 8192
	spaceHeadroom       = 64 << 20
)

// Options configures the optional ffmpeg Transcode processor. Binary is aPATH
// name or a local path; an empty value means "ffmpeg" and a failed probe only
// disables this processor, never the daemon.
type Options struct {
	Binary        string
	Threads       int
	MaxConcurrent int
	Timeout       time.Duration
	OnProgress    func(key, value string)

	// Test seams: the daemon never sets these. testPrefix lets unit tests run
	// the Go test binary as a fake ffmpeg; testSem isolates the concurrency
	// limiter between independent test cases.
	testPrefix []string
	testSem    chan struct{}
}

// Transcode implements core.PostProcessor with a fixed, whitelisted ffmpeg
// command template. It is deliberately NOT registered by NewRegistry: the
// daemon opts in only after a successful capability probe.
type Transcode struct {
	binary    string
	prefix    []string
	threads   int
	timeout   time.Duration
	sem       chan struct{}
	onStep    func(key, value string)
	version   string
	major     int
	buildconf string
	encoders  map[string]bool
	hwaccels  []string

	mu       sync.Mutex
	progress map[string]string
}

var (
	sharedOnce sync.Once
	sharedSem  chan struct{}
)

// sharedSlots returns the daemon-wide transcode concurrency limiter. The
// capacity is fixed by the first construction, matching the single-registry
// daemon topology; tests inject their own channel instead of touching it.
func sharedSlots(n int) chan struct{} {
	sharedOnce.Do(func() {
		if n < 1 {
			n = defaultConcurrent
		}
		sharedSem = make(chan struct{}, n)
	})
	return sharedSem
}

// NewTranscode resolves and probes the binary. A probe failure is returned as
// E_CAPABILITY_UNAVAILABLE, which the daemon logs and degrades from.
func NewTranscode(ctx context.Context, o Options) (*Transcode, error) {
	binary := strings.TrimSpace(o.Binary)
	if binary == "" {
		binary = defaultFFmpegBinary
	}
	if strings.ContainsAny(binary, "\x00\r\n") {
		return nil, core.Err(core.E_INVALID_INPUT, "ffmpeg binary contains control characters")
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "ffmpeg binary %q not found: %v", binary, err)
	}
	threads := o.Threads
	if threads == 0 {
		threads = defaultThreads
	}
	if threads < 1 || threads > 64 {
		return nil, core.Err(core.E_INVALID_INPUT, "ffmpegThreads must be within 1..64")
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	// The config layer constrains operators to 1m..24h; the processor itself
	// also accepts shorter test timeouts so cancellation stays deterministic.
	if timeout < time.Second || timeout > 24*time.Hour {
		return nil, core.Err(core.E_INVALID_INPUT, "ffmpegTimeout must be within 1s..24h")
	}
	sem := o.testSem
	if sem == nil {
		sem = sharedSlots(o.MaxConcurrent)
	}
	t := &Transcode{
		binary: resolved, prefix: append([]string(nil), o.testPrefix...),
		threads: threads, timeout: timeout, sem: sem, onStep: o.OnProgress,
		encoders: map[string]bool{}, progress: map[string]string{},
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := t.probe(probeCtx); err != nil {
		return nil, err
	}
	return t, nil
}

// Name implements core.PostProcessor.
func (t *Transcode) Name() string { return TranscodeName }

// Version returns the probed ffmpeg version string (empty before probing).
func (t *Transcode) Version() string { return t.version }

// Sidecars reports the optional external binary for GET /capabilities. GPL
// builds are labelled, never silently presented as LGPL-clean.
func (t *Transcode) Sidecars() []string {
	label := "LGPL"
	if strings.Contains(t.buildconf, "--enable-gpl") {
		label = "GPL"
	}
	return []string{fmt.Sprintf("ffmpeg@%s(%s)", t.version, label)}
}

// availableBytes is a seam so tests can simulate a full disk without touching
// the real filesystem; production uses the OS-specific Statfs/FreeSpace call.
var availableBytes = availableBytesOS

// Progress returns the last key=value snapshot parsed from ffmpeg's stdout.
func (t *Transcode) Progress() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.progress))
	for k, v := range t.progress {
		out[k] = v
	}
	return out
}

func (t *Transcode) probe(ctx context.Context) error {
	out, err := t.runProbe(ctx, "-hide_banner", "-version")
	if err != nil {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "ffmpeg -version failed: %v", err)
	}
	first := firstLine(out)
	const prefix = "ffmpeg version "
	if !strings.HasPrefix(first, prefix) {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "unrecognized ffmpeg version output: %q", first)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(first, prefix))
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "ffmpeg version is empty")
	}
	major, err := strconv.Atoi(strings.Split(fields[0], ".")[0])
	if err != nil || major < 4 || major > 12 {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "unsupported ffmpeg major version %q", fields[0])
	}
	t.version, t.major = fields[0], major

	conf, err := t.runProbe(ctx, "-hide_banner", "-buildconf")
	if err != nil {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "ffmpeg -buildconf failed: %v", err)
	}
	if strings.Contains(conf, "--enable-nonfree") {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"ffmpeg build enables --enable-nonfree (not redistributable); use an LGPL/GPL build")
	}
	t.buildconf = truncate(conf, 4096)

	enc, err := t.runProbe(ctx, "-hide_banner", "-encoders")
	if err != nil {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE, "ffmpeg -encoders failed: %v", err)
	}
	for _, name := range []string{"libx264", "aac", "libvpx-vp9", "libopus"} {
		t.encoders[name] = strings.Contains(enc, name)
	}
	if !t.encoders["libx264"] || !t.encoders["aac"] {
		return core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"ffmpeg build lacks required mp4/mkv encoders (libx264, aac)")
	}

	hw, err := t.runProbe(ctx, "-hide_banner", "-hwaccels")
	if err == nil {
		for _, line := range strings.Split(hw, "\n") {
			if v := strings.TrimSpace(line); v != "" && !strings.HasPrefix(v, "Hardware") {
				t.hwaccels = append(t.hwaccels, v)
			}
		}
	}
	return nil
}

func (t *Transcode) runProbe(ctx context.Context, args ...string) (string, error) {
	all := append(append([]string(nil), t.prefix...), args...)
	cmd := exec.CommandContext(ctx, t.binary, all...)
	cmd.Env = filteredEnv()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// filteredEnv implements ADR-0008's "do not inherit the core environment":
// only a small allowlist needed by the CLI and the test helper is passed.
// Credentials, proxy settings and every other daemon variable stay inside ud.
func filteredEnv() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USERPROFILE": true, "TMPDIR": true,
		"TEMP": true, "TMP": true, "SYSTEMROOT": true, "PATHEXT": true,
		"COMSPEC": true, "LANG": true, "LC_ALL": true,
		"UD_TEST_FFMPEG": true, "UD_TEST_FFMPEG_MODE": true, "UD_TEST_FFMPEG_PID_FILE": true,
	}
	out := make([]string, 0, 8)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if allowed[strings.ToUpper(key)] {
			out = append(out, kv)
		}
	}
	return out
}

// Process accepts exactly one already-downloaded local file and writes a
// distinct `<target>.transcode.part`; the core performs the atomic rename.
func (t *Transcode) Process(ctx context.Context, in core.LocalFiles, out core.OutputSpec) (*core.ProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
	}
	if len(in.Paths) != 1 {
		return nil, core.Err(core.E_INVALID_INPUT, "Transcode requires exactly one local input file")
	}
	input := in.Paths[0]
	if err := validateLocalPath(input); err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", err)
	}
	st, err := os.Stat(input)
	if err != nil || st.IsDir() {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "input file unavailable: %v", err)
	}
	if out.Target == "" || strings.Contains(out.Target, "://") || !filepath.IsAbs(out.Target) {
		return nil, core.Err(core.E_INVALID_INPUT, "Transcode output target must be an absolute local path")
	}
	suffix := out.TempSuffix
	if suffix == "" {
		suffix = ".part"
	}
	if strings.ContainsAny(suffix, "/\\") || suffix == "." || suffix == ".." {
		return nil, core.Err(core.E_INVALID_INPUT, "invalid temp suffix %q", suffix)
	}
	outPath := out.Target + ".transcode" + suffix
	if outPath == input {
		return nil, core.Err(core.E_INVALID_INPUT, "transcode output must differ from the downloaded input")
	}
	ext := strings.ToLower(filepath.Ext(out.Target))
	args, err := t.command(ext, input, outPath)
	if err != nil {
		return nil, err
	}
	need := requiredSpace(st.Size())
	free, err := availableBytes(filepath.Dir(outPath))
	if err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "free-space check failed: %v", err)
	}
	if free < need {
		return nil, core.Err(core.E_STORAGE_FULL,
			"need %d bytes for transcode (input+output+headroom), %d available", need, free)
	}

	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "%v", ctx.Err())
	}

	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, t.binary, append(append([]string(nil), t.prefix...), args...)...)
	cmd.Env = filteredEnv()
	cmd.Dir = filepath.Dir(outPath)
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "ffmpeg start: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	var scanErr error
	go func() {
		defer wg.Done()
		scanErr = t.readProgress(stdout)
	}()
	var tail lockedTail
	go func() {
		defer wg.Done()
		// Drain the ENTIRE stderr stream: stopping at the tail limit would let a
		// chatty ffmpeg block on a full pipe. lockedTail keeps only the last N bytes.
		_, _ = io.Copy(&tail, stderr)
	}()
	waitErr := cmd.Wait()
	wg.Wait()

	if waitErr != nil || scanErr != nil {
		_ = os.Remove(outPath)
		detail := strings.TrimSpace(tail.String())
		if runCtx.Err() != nil {
			detail = fmt.Sprintf("%v; %s", runCtx.Err(), detail)
		}
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "ffmpeg transcode failed: %v; %s", waitErr, truncateTail(detail, 2048))
	}
	res, err := os.Stat(outPath)
	if err != nil || res.Size() == 0 {
		_ = os.Remove(outPath)
		return nil, core.Err(core.E_POSTPROCESS_FAILED, "ffmpeg produced no non-empty output: %v", err)
	}
	// The core, never the plugin, renames the .part file to its final name.
	return &core.ProcessResult{Outputs: []string{outPath}, TempPaths: []string{outPath}}, nil
}

func (t *Transcode) command(ext, input, output string) ([]string, error) {
	threads := strconv.Itoa(t.threads)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-threads", threads, "-i", input, "-threads", threads}
	// ADR-0011: FFmpeg 9 removed -vsync/-top; never emit removed flags there.
	if t.major >= 9 {
		args = append(args, "-fps_mode", "passthrough")
	} else {
		args = append(args, "-vsync", "0")
	}
	// The output name is always `<target>.transcode.part`, so ffmpeg cannot
	// infer the container from the extension: the muxer must be explicit.
	var format string
	switch ext {
	case ".mp4", ".mov":
		if !t.encoders["libx264"] || !t.encoders["aac"] {
			return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "missing libx264/aac encoder")
		}
		format = "mp4"
		args = append(args, "-map", "0:v:0?", "-map", "0:a:0?",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart",
			"-max_muxing_queue_size", "1024")
	case ".mkv":
		if !t.encoders["libx264"] || !t.encoders["aac"] {
			return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "missing libx264/aac encoder")
		}
		format = "matroska"
		args = append(args, "-map", "0:v:0?", "-map", "0:a:0?",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k", "-max_muxing_queue_size", "1024")
	case ".webm":
		if !t.encoders["libvpx-vp9"] || !t.encoders["libopus"] {
			return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE, "missing libvpx-vp9/libopus encoder")
		}
		format = "webm"
		args = append(args, "-map", "0:v:0?", "-map", "0:a:0?",
			"-c:v", "libvpx-vp9", "-b:v", "0", "-crf", "33",
			"-c:a", "libopus", "-b:a", "96k", "-max_muxing_queue_size", "1024")
	default:
		return nil, core.Err(core.E_INVALID_INPUT, "unsupported transcode target extension %q", ext)
	}
	args = append(args, "-progress", "pipe:1", "-nostats", "-f", format, output)
	return args, nil
}

func (t *Transcode) readProgress(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "frame", "out_time_ms", "speed", "progress", "fps", "time":
			t.mu.Lock()
			t.progress[key] = value
			t.mu.Unlock()
			if t.onStep != nil {
				t.onStep(key, value)
			}
		}
	}
	return sc.Err()
}

func validateLocalPath(p string) error {
	if p == "" || strings.ContainsAny(p, "\x00\r\n") || strings.Contains(p, "://") {
		return fmt.Errorf("not a local file path")
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// truncateTail keeps the LAST n bytes: ffmpeg writes its final diagnosis at
// the end of a long stderr stream, so a head-truncated message is useless.
func truncateTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func requiredSpace(input int64) uint64 {
	const max = int64(^uint64(0) >> 1)
	need := input
	if need > max/2-64<<20 {
		return uint64(max)
	}
	return uint64(need*2 + 64<<20)
}

type lockedTail struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedTail) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b.Write(p)
	if w.b.Len() > stderrTailBytes {
		w.b.Next(w.b.Len() - stderrTailBytes)
	}
	return len(p), nil
}

func (w *lockedTail) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}
