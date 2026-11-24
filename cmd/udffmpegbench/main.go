// Command udffmpegbench measures the two docs/12 §5 criteria that became
// executable only after the P2 ffmpeg Transcode path landed:
//
//   - one ffmpeg exec overhead (single local-file transcode, no network);
//   - chunk hashing and a real transcode running concurrently without either
//     one starving the other (ADR-0012 decision 7).
//
// It is an offline reproducibility harness, NOT wired into CI. Every number
// comes from a real, locally installed ffmpeg binary; if the binary is absent
// or a probe fails the report says NOT_EXECUTED instead of inventing a value.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/postproc"
)

type metric struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Target   string `json:"target"`
	Measured string `json:"measured"`
	Status   string `json:"status"` // PASS | FAIL | NOT_EXECUTED
	Met      bool   `json:"met"`
	Detail   string `json:"detail,omitempty"`
}

type report struct {
	Schema      string         `json:"schema"`
	GeneratedAt string         `json:"generated_at"`
	FFmpeg      string         `json:"ffmpeg_binary"`
	Version     string         `json:"ffmpeg_version"`
	Samples     int            `json:"samples"`
	Metrics     []metric       `json:"metrics"`
	Raw         map[string]any `json:"raw"`
	NonClaim    string         `json:"non_claim"`
}

const nonClaim = "本工具只测本机真实 ffmpeg 的 exec 开销与「哈希/转码并发不互饿」；" +
	"不替代 NFR-PERF-01 链路吞吐、L4 互操作或移动端真机指标。"

func main() {
	bin := flag.String("ffmpeg", "ffmpeg", "local ffmpeg binary (must pass the ud capability probe)")
	work := flag.String("work", filepath.Join(os.TempDir(), "udffmpegbench"), "work directory")
	samples := flag.Int("samples", 20, "number of real transcode execs to time")
	hashMB := flag.Int("hash-mb", 128, "size of the chunk-hash payload in MiB")
	out := flag.String("out", "dist/ffmpeg_bench.json", "JSON report path")
	flag.Parse()

	rep := run(*bin, *work, *samples, *hashMB)
	blob, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "udffmpegbench: marshal: %v\n", err)
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err == nil {
		if werr := os.WriteFile(*out, append(blob, '\n'), 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "udffmpegbench: write %s: %v\n", *out, werr)
		}
	}
	fmt.Println(string(blob))
	for _, m := range rep.Metrics {
		if m.Status == "FAIL" {
			os.Exit(1)
		}
	}
}

func run(bin, work string, samples, hashMB int) report {
	rep := report{
		Schema: "ud.ffmpegbench/v1", GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		FFmpeg: bin, Samples: samples, Metrics: []metric{}, Raw: map[string]any{}, NonClaim: nonClaim,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := os.MkdirAll(work, 0o755); err != nil {
		rep.Metrics = append(rep.Metrics, metric{ID: "NFR-PERF-FFMPEG", Name: "ffmpeg exec 开销",
			Target: "单次 200–500ms 可接受", Status: "NOT_EXECUTED",
			Detail: "work directory: " + err.Error()})
		return rep
	}
	tp, err := postproc.NewTranscode(ctx, postproc.Options{
		Binary: bin, Threads: 1, MaxConcurrent: 1, Timeout: 2 * time.Minute,
	})
	if err != nil {
		for _, id := range []string{"NFR-PERF-FFMPEG", "NFR-PERF-HASH-CPU"} {
			rep.Metrics = append(rep.Metrics, metric{ID: id, Status: "NOT_EXECUTED",
				Measured: "未执行", Detail: "ffmpeg 能力探测未通过: " + err.Error()})
		}
		return rep
	}
	rep.Version = tp.Version()
	source := filepath.Join(work, "source.mp4")
	if err := makeSource(ctx, bin, source); err != nil {
		for _, id := range []string{"NFR-PERF-FFMPEG", "NFR-PERF-HASH-CPU"} {
			rep.Metrics = append(rep.Metrics, metric{ID: id, Status: "NOT_EXECUTED",
				Measured: "未执行", Detail: "生成测试视频失败: " + err.Error()})
		}
		return rep
	}

	durations := make([]float64, 0, samples)
	for i := 0; i < samples; i++ {
		target := filepath.Join(work, fmt.Sprintf("exec-%03d.mp4", i))
		start := time.Now()
		res, perr := tp.Process(ctx, core.LocalFiles{BaseDir: filepath.Dir(source), Paths: []string{source}}, core.OutputSpec{Target: target, TempSuffix: ".part"})
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if perr != nil {
			rep.Metrics = append(rep.Metrics, metric{ID: "NFR-PERF-FFMPEG", Name: "ffmpeg exec 开销",
				Target: "单次 200–500ms 可接受", Status: "FAIL",
				Measured: fmt.Sprintf("第 %d 次转码失败", i+1), Detail: perr.Error()})
			return rep
		}
		for _, p := range res.Outputs {
			_ = os.Remove(p)
		}
		durations = append(durations, elapsed)
	}
	sort.Float64s(durations)
	median := percentile(durations, 50)
	p95 := percentile(durations, 95)
	rep.Raw["exec_ms_min"] = round1(durations[0])
	rep.Raw["exec_ms_median"] = round1(median)
	rep.Raw["exec_ms_p95"] = round1(p95)
	rep.Raw["exec_ms_max"] = round1(durations[len(durations)-1])
	status := "PASS"
	if median > 500 {
		status = "FAIL"
	}
	rep.Metrics = append(rep.Metrics, metric{
		ID: "NFR-PERF-FFMPEG", Name: "ffmpeg exec 开销", Target: "单次 200–500ms 可接受",
		Measured: fmt.Sprintf("中位 %.1fms / P95 %.1fms / 最大 %.1fms（%d 次真实转码）",
			median, p95, durations[len(durations)-1], len(durations)),
		Status: status, Met: status == "PASS",
		Detail: fmt.Sprintf("输入为本地生成的 2s/320x240 测试视频；计时覆盖 postproc.Transcode.Process 的整次 exec，"+
			"不含能力探测与测试视频生成；目标按中位值判定，低于 200ms 视为优于目标。ffmpeg 构建：%s", tp.Sidecars()),
	})

	hashPath := filepath.Join(work, "hash.bin")
	if err := makeHashPayload(hashPath, hashMB); err != nil {
		rep.Metrics = append(rep.Metrics, metric{ID: "NFR-PERF-HASH-CPU", Name: "分片哈希不与转码互相饿死",
			Target: "并发信号量验证", Status: "NOT_EXECUTED",
			Measured: "未执行", Detail: "生成哈希载荷失败: " + err.Error()})
		return rep
	}
	// Baseline: hash the payload alone so the concurrent run has a reference.
	var soloChunks, soloBytes atomic.Int64
	soloStart := time.Now()
	if err := hashChunks(ctx, hashPath, 4<<20, &soloChunks, &soloBytes); err != nil {
		rep.Metrics = append(rep.Metrics, metric{ID: "NFR-PERF-HASH-CPU", Name: "分片哈希不与转码互相饿死",
			Target: "并发信号量验证", Status: "NOT_EXECUTED",
			Measured: "未执行", Detail: "单跑哈希失败: " + err.Error()})
		return rep
	}
	soloMS := float64(time.Since(soloStart).Microseconds()) / 1000

	// A longer local source keeps the real encoder busy long enough to overlap.
	longSource := filepath.Join(work, "source-long.mp4")
	if err := makeLongSource(ctx, bin, longSource); err != nil {
		rep.Metrics = append(rep.Metrics, metric{ID: "NFR-PERF-HASH-CPU", Name: "分片哈希不与转码互相饿死",
			Target: "并发信号量验证", Status: "NOT_EXECUTED",
			Measured: "未执行", Detail: "生成长测试视频失败: " + err.Error()})
		return rep
	}

	var chunks atomic.Int64
	var bytesDone atomic.Int64
	var transcodeOK atomic.Bool
	var transcodeErr error
	var hashMS, transcodeMS float64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		target := filepath.Join(work, "concurrent.mp4")
		start := time.Now()
		res, err := tp.Process(ctx, core.LocalFiles{BaseDir: filepath.Dir(longSource), Paths: []string{longSource}}, core.OutputSpec{Target: target, TempSuffix: ".part"})
		transcodeMS = float64(time.Since(start).Microseconds()) / 1000
		if err == nil {
			for _, p := range res.Outputs {
				_ = os.Remove(p)
			}
			transcodeOK.Store(true)
		} else {
			transcodeErr = err
		}
	}()
	go func() {
		defer wg.Done()
		start := time.Now()
		if err := hashChunks(ctx, hashPath, 4<<20, &chunks, &bytesDone); err != nil {
			transcodeErr = err
		}
		hashMS = float64(time.Since(start).Microseconds()) / 1000
	}()
	wg.Wait()

	soloMBps := float64(soloBytes.Load()) / 1e6 / (soloMS / 1000)
	concMBps := float64(bytesDone.Load()) / 1e6 / (hashMS / 1000)
	ratio := 0.0
	if soloMBps > 0 {
		ratio = concMBps / soloMBps
	}
	rep.Raw["hash_solo_ms"] = round1(soloMS)
	rep.Raw["hash_concurrent_ms"] = round1(hashMS)
	rep.Raw["hash_solo_mib_s"] = round1(soloMBps)
	rep.Raw["hash_concurrent_mib_s"] = round1(concMBps)
	rep.Raw["hash_throughput_ratio"] = round1(ratio * 100)
	rep.Raw["transcode_concurrent_ms"] = round1(transcodeMS)
	rep.Raw["hash_chunks_total"] = chunks.Load()
	rep.Raw["hash_bytes_total"] = bytesDone.Load()

	// Thresholds are deliberately loose: the point is "neither side is reduced
	// to a halt", not a throughput claim. Host A is a 1 vCPU box by design.
	coStatus := "PASS"
	detail := fmt.Sprintf("4MiB 分片 SHA-256 与真实 ffmpeg 转码在同一进程内并发；"+
		"哈希单跑 %.1fms（%.1f MiB/s），并发 %.1fms（%.1f MiB/s，为单跑的 %.0f%%）；转码并发耗时 %.1fms。",
		soloMS, soloMBps, hashMS, concMBps, ratio*100, transcodeMS)
	if transcodeErr != nil || !transcodeOK.Load() || chunks.Load() == 0 || bytesDone.Load() == 0 {
		coStatus = "FAIL"
		detail = fmt.Sprintf("并发验证失败: transcodeOK=%v chunks=%d bytes=%d err=%v",
			transcodeOK.Load(), chunks.Load(), bytesDone.Load(), transcodeErr)
	} else if ratio < 0.25 {
		coStatus = "FAIL"
		detail += " 哈希吞吐被压到单跑的 25% 以下，判定为互饿。"
	}
	rep.Metrics = append(rep.Metrics, metric{
		ID: "NFR-PERF-HASH-CPU", Name: "分片哈希不与转码互相饿死", Target: "并发吞吐 ≥ 单跑 25% 且两侧均完成",
		Measured: fmt.Sprintf("哈希 %d 片 / %.1f MiB，并发吞吐为单跑 %.0f%%，转码 %.1fms 完成",
			chunks.Load(), float64(bytesDone.Load())/(1<<20), ratio*100, transcodeMS),
		Status: coStatus, Met: coStatus == "PASS", Detail: detail,
	})
	return rep
}

func makeSource(ctx context.Context, bin, path string) error {
	// Deterministic local video: no URL, no sidecar beyond the measured ffmpeg.
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", path}
	cmd := exec.CommandContext(ctx, bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg lavfi: %v: %s", err, truncate(string(out), 512))
	}
	return nil
}

// makeLongSource builds a longer clip so a real encoder runs for seconds rather
// than milliseconds; otherwise the concurrency window is too short to observe.
func makeLongSource(ctx context.Context, bin, path string) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=10:size=640x360:rate=25",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", path}
	cmd := exec.CommandContext(ctx, bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg lavfi long: %v: %s", err, truncate(string(out), 512))
	}
	return nil
}

func makeHashPayload(path string, mb int) error {
	if mb < 4 {
		mb = 4
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	block := make([]byte, 1<<20)
	for i := range block {
		block[i] = byte(i)
	}
	for i := 0; i < mb; i++ {
		if _, err := f.Write(block); err != nil {
			return err
		}
	}
	return nil
}

func hashChunks(ctx context.Context, path string, chunkSize int, chunks, total *atomic.Int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, chunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			_ = sha256.Sum256(buf[:n])
			chunks.Add(1)
			total.Add(int64(n))
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	i := (len(sorted) - 1) * p / 100
	return sorted[i]
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
