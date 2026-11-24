package postproc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// fakeHelperOptions drives the Go test binary as an ffmpeg stand-in. No real
// ffmpeg binary is needed in CI; every assertion here is about ud's contract,
// not about ffmpeg's codec behaviour.
func fakeHelperOptions(t *testing.T, mode string) Options {
	t.Helper()
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("UD_TEST_FFMPEG", "1")
	t.Setenv("UD_TEST_FFMPEG_MODE", mode)
	sem := make(chan struct{}, 1)
	return Options{
		Binary: bin, Threads: 1, MaxConcurrent: 1, Timeout: 5 * time.Second,
		testPrefix: []string{"-test.run=TestTranscodeHelperProcess", "--"},
		testSem:    sem,
	}
}

func TestTranscodeProbeAndCapabilities(t *testing.T) {
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	if tp.Name() != TranscodeName || tp.Version() != "7.0.2-test" || tp.major != 7 {
		t.Fatalf("unexpected probe result: name=%q version=%q major=%d", tp.Name(), tp.Version(), tp.major)
	}
	sidecars := tp.Sidecars()
	if len(sidecars) != 1 || !strings.Contains(sidecars[0], "GPL") {
		t.Fatalf("GPL build must be labelled explicitly: %v", sidecars)
	}
	r := NewRegistry()
	r.Register(tp)
	if got := r.Sidecars(); len(got) != 1 || got[0] != sidecars[0] {
		t.Fatalf("registry sidecars = %v, want %v", got, sidecars)
	}
}

func TestTranscodeMissingBinaryDegrades(t *testing.T) {
	// The daemon relies on this being a capability error, not a fatal one:
	// downloads must keep working when the operator never installed ffmpeg.
	_, err := NewTranscode(context.Background(), Options{Binary: "ud-no-such-ffmpeg-binary"})
	if err == nil || core.AsError(err).Code != core.E_CAPABILITY_UNAVAILABLE {
		t.Fatalf("missing binary must degrade with E_CAPABILITY_UNAVAILABLE, got %v", err)
	}
}

func TestTranscodeRejectsNonFreeBuild(t *testing.T) {
	if _, err := NewTranscode(context.Background(), fakeHelperOptions(t, "nonfree")); err == nil ||
		core.AsError(err).Code != core.E_CAPABILITY_UNAVAILABLE {
		t.Fatalf("nonfree build must degrade, got %v", err)
	}
}

func TestTranscodeRejectsMissingEncoders(t *testing.T) {
	if _, err := NewTranscode(context.Background(), fakeHelperOptions(t, "noenc")); err == nil ||
		core.AsError(err).Code != core.E_CAPABILITY_UNAVAILABLE {
		t.Fatalf("missing encoders must degrade, got %v", err)
	}
}

func TestTranscodeSelectsExplicitMuxer(t *testing.T) {
	// The output is always `<target>.transcode.part`, so ffmpeg cannot infer the
	// container from the extension. A real ffmpeg 5.1 run failed with
	// "Unable to find a suitable output format" until -f was emitted.
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	tp.encoders["libvpx-vp9"] = true
	tp.encoders["libopus"] = true
	for ext, want := range map[string]string{
		".mp4": "mp4", ".mov": "mp4", ".mkv": "matroska", ".webm": "webm",
	} {
		args, err := tp.command(ext, "/in/src.bin", "/out/movie"+ext+".transcode.part")
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		if len(args) < 3 || args[len(args)-3] != "-f" || args[len(args)-2] != want {
			t.Fatalf("%s: muxer args = %v, want trailing `-f %s <output>`", ext, args[len(args)-4:], want)
		}
		if args[len(args)-1] != "/out/movie"+ext+".transcode.part" {
			t.Fatalf("%s: output must stay the last argument: %v", ext, args[len(args)-2:])
		}
	}
	if _, err := tp.command(".avi", "/in/a", "/out/b.avi.part"); err == nil ||
		core.AsError(err).Code != core.E_INVALID_INPUT {
		t.Fatalf("unsupported container must be rejected, got %v", err)
	}
}

func TestTranscodeWritesPartAndParsesProgress(t *testing.T) {
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mp4.part")
	data := []byte("downloaded-bytes")
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "movie.mp4")
	steps := map[string]string{}
	tp.onStep = func(k, v string) { steps[k] = v }
	res, err := tp.Process(context.Background(),
		core.LocalFiles{BaseDir: dir, Paths: []string{input}},
		core.OutputSpec{Target: target, TempSuffix: ".part"})
	if err != nil {
		t.Fatal(err)
	}
	wantPart := target + ".transcode.part"
	if len(res.Outputs) != 1 || res.Outputs[0] != wantPart {
		t.Fatalf("outputs = %v, want %q", res.Outputs, wantPart)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("plugin must never create the final target")
	}
	if got, err := os.ReadFile(wantPart); err != nil || string(got) != "transcoded" {
		t.Fatalf("output = %q, %v", got, err)
	}
	if got, err := os.ReadFile(input); err != nil || string(got) != string(data) {
		t.Fatalf("input was modified: %q, %v", got, err)
	}
	if steps["progress"] != "end" || tp.Progress()["out_time_ms"] != "1000" {
		t.Fatalf("progress not parsed: steps=%v snapshot=%v", steps, tp.Progress())
	}
}

func TestTranscodeDrainsStderrAndCleansFailure(t *testing.T) {
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "fail-chatty"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mkv.part")
	if err := os.WriteFile(input, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target := filepath.Join(dir, "movie.mkv")
	_, err = tp.Process(ctx, core.LocalFiles{Paths: []string{input}},
		core.OutputSpec{Target: target, TempSuffix: ".part"})
	if err == nil || core.AsError(err).Code != core.E_POSTPROCESS_FAILED {
		t.Fatalf("want E_POSTPROCESS_FAILED, got %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr tail missing from error: %v", err)
	}
	if _, err := os.Stat(target + ".transcode.part"); !os.IsNotExist(err) {
		t.Fatal("failed transcode left a partial output")
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatalf("failed transcode removed its input: %v", err)
	}
}

func TestTranscodeRejectsBadInputs(t *testing.T) {
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "a.mp4.part")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   core.LocalFiles
		out  core.OutputSpec
		code string
	}{
		{"url input", core.LocalFiles{Paths: []string{"https://example.com/a.mp4"}}, core.OutputSpec{Target: file}, core.E_POSTPROCESS_FAILED},
		{"two inputs", core.LocalFiles{Paths: []string{file, file}}, core.OutputSpec{Target: file}, core.E_INVALID_INPUT},
		{"relative target", core.LocalFiles{Paths: []string{file}}, core.OutputSpec{Target: "out.mp4"}, core.E_INVALID_INPUT},
		{"bad suffix", core.LocalFiles{Paths: []string{file}}, core.OutputSpec{Target: file, TempSuffix: "a/b"}, core.E_INVALID_INPUT},
		{"unsupported ext", core.LocalFiles{Paths: []string{file}}, core.OutputSpec{Target: filepath.Join(dir, "a.avi")}, core.E_INVALID_INPUT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tp.Process(context.Background(), tc.in, tc.out)
			if err == nil || core.AsError(err).Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestTranscodeChecksFreeSpace(t *testing.T) {
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "a.mp4.part")
	if err := os.WriteFile(input, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := availableBytes
	availableBytes = func(string) (uint64, error) { return 1, nil }
	t.Cleanup(func() { availableBytes = old })
	_, err = tp.Process(context.Background(), core.LocalFiles{Paths: []string{input}},
		core.OutputSpec{Target: filepath.Join(dir, "a.mp4"), TempSuffix: ".part"})
	if err == nil || core.AsError(err).Code != core.E_STORAGE_FULL {
		t.Fatalf("want E_STORAGE_FULL, got %v", err)
	}
}

// TestTranscodeSemaphoreLeavesHashRunning is a deterministic L1 guard for
// ADR-0012 decision 7: the transcode slot never acquires the download/hash
// budget. It is NOT the real-ffmpeg NFR metric in docs/12 §5.
func TestTranscodeSemaphoreLeavesHashRunning(t *testing.T) {
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "slow"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "a.mp4.part")
	if err := os.WriteFile(input, []byte("hash-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UD_TEST_FFMPEG_PID_FILE", filepath.Join(dir, "helper.pid"))
	var hashed int64
	var mu sync.Mutex
	stop := make(chan struct{})
	var hashWG sync.WaitGroup
	hashWG.Add(1)
	go func() {
		defer hashWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				b, err := os.ReadFile(input)
				if err == nil {
					mu.Lock()
					hashed += int64(len(b))
					mu.Unlock()
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = tp.Process(ctx, core.LocalFiles{Paths: []string{input}},
		core.OutputSpec{Target: filepath.Join(dir, "a.mp4"), TempSuffix: ".part"})
	close(stop)
	hashWG.Wait()
	if err == nil {
		t.Fatal("slow helper should time out")
	}
	mu.Lock()
	count := hashed
	mu.Unlock()
	if count <= 0 {
		t.Fatal("hashing was starved while transcode held its own slot")
	}
}

func TestTranscodeKillsProcessTreeOnCancel(t *testing.T) {
	if strings.HasPrefix(strings.ToLower(os.Getenv("GOOS")), "windows") {
		t.Skip("Unix process-group assertion")
	}
	tp, err := NewTranscode(context.Background(), fakeHelperOptions(t, "slow"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "a.mp4.part")
	if err := os.WriteFile(input, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "helper.pid")
	t.Setenv("UD_TEST_FFMPEG_PID_FILE", pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tp.Process(ctx, core.LocalFiles{Paths: []string{input}},
			core.OutputSpec{Target: filepath.Join(dir, "a.mp4"), TempSuffix: ".part"})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && pid > 0 {
				cancel()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("canceled transcode returned success")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("cancel did not return promptly")
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never wrote its pid")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTranscodeHelperProcess(t *testing.T) {
	if os.Getenv("UD_TEST_FFMPEG") != "1" {
		return
	}
	args := os.Args
	if i := indexArg(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	mode := os.Getenv("UD_TEST_FFMPEG_MODE")
	if hasArg(args, "-version") {
		fmt.Println("ffmpeg version 7.0.2-test Copyright (c) fake")
		os.Exit(0)
	}
	if hasArg(args, "-buildconf") {
		if mode == "nonfree" {
			fmt.Println("configuration: --enable-gpl --enable-nonfree")
		} else {
			fmt.Println("configuration: --enable-gpl --prefix=/fake")
		}
		os.Exit(0)
	}
	if hasArg(args, "-encoders") {
		if mode != "noenc" {
			fmt.Println(" V....D libx264               H.264 / AVC")
			fmt.Println(" A....D aac                   AAC")
			fmt.Println(" V....D libvpx-vp9            VP9")
			fmt.Println(" A....D libopus               Opus")
		}
		os.Exit(0)
	}
	if hasArg(args, "-hwaccels") {
		fmt.Println("Hardware acceleration methods:\nvideotoolbox")
		os.Exit(0)
	}
	input, output := "", ""
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			input = args[i+1]
		}
	}
	if len(args) > 0 {
		output = args[len(args)-1]
	}
	if mode == "fail-chatty" {
		fmt.Fprintln(os.Stderr, strings.Repeat("x", 256<<10)+"\nboom")
		os.Exit(3)
	}
	if mode == "slow" {
		if p := os.Getenv("UD_TEST_FFMPEG_PID_FILE"); p != "" {
			_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
		time.Sleep(2 * time.Second)
	}
	if input == "" || output == "" {
		fmt.Fprintln(os.Stderr, "helper missing -i/output")
		os.Exit(2)
	}
	if b, err := os.ReadFile(input); err == nil && string(b) == "" {
		fmt.Fprintln(os.Stderr, "empty input")
		os.Exit(2)
	}
	if err := os.WriteFile(output, []byte("transcoded"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println("frame=1")
	fmt.Println("out_time_ms=1000")
	fmt.Println("speed=1.0x")
	fmt.Println("progress=end")
	os.Exit(0)
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func indexArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
