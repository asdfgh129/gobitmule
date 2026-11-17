// Command udbench measures the docs/12 §5 performance criteria that need a
// real daemon rather than a synthetic micro-benchmark:
//
//   - 1000-task resident memory increment (<= 300MB, long-soak sampled);
//   - startup time including restoring 1000 tasks (<= 3s);
//   - task creation -> first transferred byte latency (P99 <= 1s);
//
// Everything runs against the REAL `udd` binary as a child process, driven over
// the REAL REST API, downloading over the REAL HTTP Range transport from a local
// fixture server. Nothing is stubbed: the RSS numbers come from the operating
// system (Linux /proc, macOS ps), the task counts come from GET /api/v1/tasks.
//
// Metrics that cannot be produced honestly in this environment are emitted as
// NOT_EXECUTED with the blocking reason (ffmpeg exec overhead and the
// hash/transcode coexistence check need a real ffmpeg; mobile BT memory needs a
// real handset). The gigabit throughput criterion lives in `cmd/udperf` and is
// reported here only as a pointer.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	schemaVersion = "ud.bench/v1"
	// memTargetMB is the docs/12 §5 resident-memory increment ceiling.
	memTargetMB = 300.0
	// startupTarget is the cold-start ceiling including 1000 restored tasks.
	startupTarget = 3 * time.Second
	// createP99Target is the task-creation -> first-byte ceiling.
	createP99Target = time.Second
	// soakSpecified is the long-soak window the criterion asks for. A shorter
	// window still gets measured and reported, but can never be a PASS.
	soakSpecified = 2 * time.Hour
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

type hostInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	NumCPU    int    `json:"num_cpu"`
	Kernel    string `json:"kernel,omitempty"`
	UptimeWin string `json:"note,omitempty"`
}

type report struct {
	Schema      string         `json:"schema"`
	GeneratedAt string         `json:"generated_at"`
	Host        hostInfo       `json:"host"`
	UDDBinary   string         `json:"udd_binary"`
	Tasks       int            `json:"tasks"`
	SoakSeconds float64        `json:"soak_seconds"`
	Metrics     []metric       `json:"metrics"`
	Raw         map[string]any `json:"raw"`
	NonClaim    string         `json:"non_claim"`
}

const nonClaim = "本报告只覆盖 docs/12 §5 中可在单机（真实 udd 进程 + 真实 REST API + 真实 HTTP 传输）" +
	"复跑的指标；千兆/百兆链路吞吐见 docs/20 与 cmd/udperf，ffmpeg 与移动端指标未执行，" +
	"不得据本报告宣称 §5 全表达标。"

func main() {
	bin := flag.String("udd", "dist/ud-linux-amd64", "path to the real udd daemon binary")
	work := flag.String("work", filepath.Join(os.TempDir(), "udbench"), "working directory (databases, payloads)")
	tasks := flag.Int("tasks", 1000, "number of resident tasks for the memory/startup phases")
	soak := flag.Duration("soak", soakSpecified, "long-soak window for the memory sampling")
	sample := flag.Duration("sample", 30*time.Second, "soak RSS sampling interval")
	latencyN := flag.Int("latency-samples", 120, "how many sequential creations to time for the P99")
	out := flag.String("out", "dist/bench.json", "write the JSON report here")
	keep := flag.Bool("keep", false, "keep the work directory (databases, download dirs)")
	flag.Parse()

	rep := run(*bin, *work, *tasks, *soak, *sample, *latencyN, *keep)
	blob, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "udbench: marshal: %v\n", err)
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err == nil {
		if werr := os.WriteFile(*out, append(blob, '\n'), 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "udbench: write %s: %v\n", *out, werr)
		}
	}
	fmt.Println(string(blob))
	for _, m := range rep.Metrics {
		if m.Status == "FAIL" {
			os.Exit(1)
		}
	}
}

func run(bin, work string, tasks int, soak, sample time.Duration, latencyN int, keep bool) report {
	rep := report{
		Schema:      schemaVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Host:        readHost(),
		UDDBinary:   bin,
		Tasks:       tasks,
		SoakSeconds: soak.Seconds(),
		Raw:         map[string]any{},
		NonClaim:    nonClaim,
	}
	if abs, err := filepath.Abs(bin); err == nil {
		rep.UDDBinary = abs
	}
	fail := func(id, name, target, detail string) {
		rep.Metrics = append(rep.Metrics, metric{ID: id, Name: name, Target: target,
			Measured: "未测得", Status: "FAIL", Met: false, Detail: detail})
	}

	if _, err := os.Stat(bin); err != nil {
		fail("NFR-PERF-BENCH", "udbench 前置条件", "需要真实 udd 二进制", "找不到 udd 二进制: "+err.Error())
		return rep
	}
	root := work
	if !keep {
		_ = os.RemoveAll(root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		fail("NFR-PERF-BENCH", "udbench 前置条件", "可写工作目录", "mkdir: "+err.Error())
		return rep
	}
	fx, err := newFixture()
	if err != nil {
		fail("NFR-PERF-BENCH", "udbench 前置条件", "本地 fixture 服务", "start fixture: "+err.Error())
		return rep
	}
	defer fx.close()
	rep.Raw["fixture_base"] = fx.base

	// ---- phase 0: empty-daemon RSS baseline --------------------------------
	// RSS availability is a platform fact, not a product fact: when the OS
	// refuses to expose a child's resident set, only the memory metrics become
	// NOT_EXECUTED — the latency and startup phases still run and are reported.
	baseDir := filepath.Join(root, "base")
	rss0 := -1.0
	rssErr := ""
	d0, err := spawnDaemon(bin, baseDir)
	if err != nil {
		rssErr = "baseline daemon: " + err.Error()
	} else {
		ready0, rerr := d0.waitReady(60 * time.Second)
		if rerr != nil {
			rssErr = "baseline daemon not ready: " + rerr.Error()
		} else {
			time.Sleep(3 * time.Second) // let the idle daemon settle
			if v, rerr := d0.rssMB(); rerr == nil {
				rss0 = v
				rep.Raw["baseline_ready_ms"] = ready0.Milliseconds()
				rep.Raw["rss_empty_mb"] = round1(rss0)
			} else {
				rssErr = "read RSS: " + rerr.Error()
			}
		}
		d0.stop()
	}

	// ---- phase 1: create -> first byte latency -----------------------------
	latDir := filepath.Join(root, "latency")
	latRep, latErr := measureCreateLatency(bin, latDir, fx, latencyN)
	if latErr != nil {
		rep.Metrics = append(rep.Metrics, metric{
			ID: "NFR-PERF-CREATE-P99", Name: "任务创建→开始传输 P99", Target: "≤ 1s",
			Measured: "未测得", Status: "FAIL", Detail: latErr.Error(),
		})
	} else {
		rep.Raw["create_latency_samples"] = len(latRep.samples)
		rep.Raw["create_latency_p50_ms"] = round1(latRep.p50)
		rep.Raw["create_latency_max_ms"] = round1(latRep.max)
		rep.Raw["create_latency_poll_ms"] = latRep.pollMs
		st := "PASS"
		if latRep.p99 > float64(createP99Target.Milliseconds()) {
			st = "FAIL"
		}
		rep.Metrics = append(rep.Metrics, metric{
			ID: "NFR-PERF-CREATE-P99", Name: "任务创建→开始传输 P99", Target: "≤ 1s",
			Measured: fmt.Sprintf("P50 %.0fms / P99 %.0fms / max %.0fms（%d 次顺序创建，轮询粒度 %dms）",
				latRep.p50, latRep.p99, latRep.max, len(latRep.samples), latRep.pollMs),
			Status: st, Met: st == "PASS",
			Detail: "不含解析：输入为直连 HTTP fixture；每次创建后等到 bytesDone>0 再取消，测的是调度分发链路本身",
		})
	}

	// ---- phase 2: 1000 resident tasks, restart, soak -----------------------
	memDir := filepath.Join(root, "mem")
	mem, err := measureResident(bin, memDir, fx, tasks)
	if err != nil {
		fail("NFR-PERF-MEM-1000", "1000 任务常驻内存增量", "≤ 300MB", err.Error())
		fail("NFR-PERF-STARTUP", "启动时间（含 1000 任务恢复）", "≤ 3s", err.Error())
	} else {
		defer mem.d.stop()
		if mem.rssBefore >= 0 {
			rep.Raw["rss_1000_mb"] = round1(mem.rssBefore)
		}
		rep.Raw["startup_ready_ms"] = mem.readyMs
		rep.Raw["startup_restored_ms"] = mem.restoredMs
		rep.Raw["tasks_restored"] = mem.restored

		inc := mem.rssBefore - rss0
		if rss0 < 0 || mem.rssBefore < 0 {
			reason := rssErr
			if reason == "" {
				reason = "1000 任务阶段无法读取 RSS"
			}
			rep.Metrics = append(rep.Metrics, metric{
				ID: "NFR-PERF-MEM-1000", Name: "1000 任务常驻内存增量", Target: "≤ 300MB",
				Measured: "未执行", Status: "NOT_EXECUTED",
				Detail: "当前平台无法读取子进程常驻内存（" + reason + "），不伪造数值",
			})
		} else {
			memStatus := "PASS"
			if inc > memTargetMB {
				memStatus = "FAIL"
			}
			rep.Metrics = append(rep.Metrics, metric{
				ID: "NFR-PERF-MEM-1000", Name: "1000 任务常驻内存增量", Target: "≤ 300MB",
				Measured: fmt.Sprintf("%.1fMB（空载 %.1fMB → 1000 任务 %.1fMB）", inc, rss0, mem.rssBefore),
				Status:   memStatus, Met: memStatus == "PASS",
				Detail: fmt.Sprintf("%d 个任务同时驻留（trickle 传输中，5 个并发槽位，其余在调度队列），RSS 取 %s",
					tasks, runtime.GOOS),
			})
		}

		// The criterion is startup INCLUDING restore, so the healthz timestamp is
		// the authoritative clock: app.Run calls Recover() BEFORE net.Listen(),
		// so a 200 from /healthz cannot be served until every task is back. The
		// task-count check is a consistency assertion, not a weaker clock.
		startStatus := "PASS"
		if !startupMet(mem.readyMs, int64(mem.restored), int64(tasks)) {
			startStatus = "FAIL"
		}
		rep.Metrics = append(rep.Metrics, metric{
			ID: "NFR-PERF-STARTUP", Name: "启动时间（含 1000 任务恢复）", Target: "≤ 3s",
			Measured: fmt.Sprintf("%dms 到 /healthz（恢复完成才监听）；另 %dms 到 GET /tasks 计数 = %d",
				mem.readyMs, mem.restoredMs, mem.restored),
			Status: startStatus, Met: startStatus == "PASS",
			Detail: fmt.Sprintf("冷启动：进程 exec → /healthz 200。app.Run 在 net.Listen 之前调用 Recover()，"+
				"故该时间已含 %d 个任务的 FromDB 恢复；同时断言 /tasks 总数为 %d", tasks, tasks),
		})

		// Long soak: the increment must stay bounded, not just start bounded.
		if soak > 0 && rss0 >= 0 && mem.rssBefore >= 0 {
			series := mem.d.sampleRSS(soak, sample, filepath.Join(root, "rss_series.txt"))
			maxRSS, minRSS, last := seriesStats(series)
			peakInc := maxRSS - rss0
			rep.Raw["soak_samples"] = len(series)
			rep.Raw["soak_rss_max_mb"] = round1(maxRSS)
			rep.Raw["soak_rss_min_mb"] = round1(minRSS)
			rep.Raw["soak_rss_last_mb"] = round1(last)
			rep.Raw["soak_peak_increment_mb"] = round1(peakInc)
			soakStatus := "PASS"
			detail := fmt.Sprintf("长稳 %s，采样 %d 次（间隔 %s），峰值增量 %.1fMB",
				shortDur(soak), len(series), shortDur(sample), peakInc)
			if soak < soakSpecified {
				soakStatus = "FAIL"
				detail += fmt.Sprintf("；长稳窗口不足规范要求的 %s，不记为达标", shortDur(soakSpecified))
			}
			if peakInc > memTargetMB {
				soakStatus = "FAIL"
			}
			rep.Metrics = append(rep.Metrics, metric{
				ID: "NFR-PERF-MEM-SOAK", Name: "1000 任务长稳内存增量", Target: "≤ 300MB（长稳 2h）",
				Measured: fmt.Sprintf("峰值增量 %.1fMB（%.1fMB → %.1fMB）", peakInc, rss0, maxRSS),
				Status:   soakStatus, Met: soakStatus == "PASS", Detail: detail,
			})
		}
	}

	// ---- criteria that cannot be measured honestly here -------------------
	rep.Metrics = append(rep.Metrics,
		metric{ID: "NFR-PERF-FFMPEG", Name: "ffmpeg exec 开销", Target: "单次 200–500ms 可接受",
			Measured: "未执行", Status: "NOT_EXECUTED",
			Detail: "P1 没有 ffmpeg 调用路径：internal/postproc 的 Transcode 仅是同接口占位（P2 落地），" +
				"因此不存在可测的 exec 开销；实验室主机也未安装 ffmpeg，不伪造数值"},
		metric{ID: "NFR-PERF-HASH-CPU", Name: "分片哈希 CPU 占用（不与转码互相饿死）", Target: "并发信号量验证",
			Measured: "未执行", Status: "NOT_EXECUTED",
			Detail: "判据要求哈希与真实转码并发执行并观测彼此进度；P1 无转码实现（P2），无法构造该并发场景"},
		metric{ID: "NFR-PERF-MOBILE-BT", Name: "移动端 BT 内存", Target: "≤ 150MB",
			Measured: "未执行", Status: "NOT_EXECUTED",
			Detail: "需要真机（Android/iOS）安装并运行 BT 会话；本环境无移动设备"},
		metric{ID: "NFR-PERF-01", Name: "HTTP 单任务吞吐（链路）", Target: "见 ADR-0022（当前百兆段 ≥10MB/s）",
			Measured: "见 docs/20（11.21MB/s，met=true）", Status: "NOT_EXECUTED",
			Detail: "由 cmd/udperf 双机模式测量，不在本工具内重复；本工具只做单机可复跑项"},
	)
	return rep
}

// ---- fixture -------------------------------------------------------------

// fixture serves two shapes:
//   - /fast/<n>.bin  : 64KiB, completes immediately (latency phase);
//   - /trickle/<n>.bin : declares 64MiB and sends one byte every 100ms, so a
//     task stays in a real transferring state for the whole soak window while
//     writing almost nothing to disk.
type fixture struct {
	ln   net.Listener
	srv  *http.Server
	base string
}

const trickleTotal = 64 << 20

func newFixture() (*fixture, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f := &fixture{ln: ln, base: "http://" + ln.Addr().String()}
	mux := http.NewServeMux()
	mux.HandleFunc("/fast/", func(w http.ResponseWriter, r *http.Request) {
		body := bytes.Repeat([]byte{0x5a}, 64<<10)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/trickle/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(trickleTotal))
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				if _, err := w.Write([]byte{0x5a}); err != nil {
					return
				}
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
			}
		}
	})
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	return f, nil
}

func (f *fixture) close() { _ = f.srv.Close() }

// ---- daemon child process -----------------------------------------------

type daemon struct {
	bin    string
	dir    string
	base   string
	cmd    *exec.Cmd
	log    *os.File
	client *http.Client
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// writeBenchConfig pins the fixture's needs: loopback URLs on arbitrary ports
// must pass the real SSRF guard, which is exactly the documented dev relaxation.
func writeBenchConfig(dir string, port int) (string, error) {
	cfg := fmt.Sprintf(`listen: 127.0.0.1:%d
downloadDir: %s
logLevel: warn
global:
  maxConcurrentTasks: 5
  maxConnectionsPerTask: 4
  chunkSize: 4MiB
security:
  allowedSchemes: ["http", "https"]
  allowedPorts: [80, 443]
  maxRedirects: 0
  blockPrivateNetworks: false
  allowLoopbackForDev: true
`, port, filepath.Join(dir, "dl"))
	path := filepath.Join(dir, "config.yaml")
	return path, os.WriteFile(path, []byte(cfg), 0o644)
}

func spawnDaemon(bin, dir string) (*daemon, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cfgPath, err := writeBenchConfig(dir, port)
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "udd.log")
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "-config", cfgPath, "-dir", filepath.Join(dir, "dl"),
		"-db", filepath.Join(dir, "ud.db"), "-listen", fmt.Sprintf("127.0.0.1:%d", port))
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		return nil, err
	}
	return &daemon{
		bin: bin, dir: dir, base: fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd: cmd, log: lf,
		client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}},
	}, nil
}

func (d *daemon) waitReady(timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := d.client.Get(d.base + "/api/v1/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return time.Since(start), nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start), fmt.Errorf("daemon not ready in %s; see %s/udd.log", timeout, d.dir)
}

func (d *daemon) stop() {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = d.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = d.cmd.Process.Kill()
		<-done
	}
	if d.log != nil {
		_ = d.log.Close()
	}
	d.cmd = nil
}

func (d *daemon) create(input string) (string, error) {
	body, _ := json.Marshal(map[string]any{"input": input})
	resp, err := d.client.Post(d.base+"/api/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create task: HTTP %d: %s", resp.StatusCode, truncate2(string(payload), 200))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

type taskView struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal int64  `json:"bytesTotal"`
}

func (d *daemon) getTask(id string) (taskView, error) {
	resp, err := d.client.Get(d.base + "/api/v1/tasks/" + id)
	if err != nil {
		return taskView{}, err
	}
	defer resp.Body.Close()
	var t taskView
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return taskView{}, err
	}
	return t, nil
}

func (d *daemon) cancel(id string) {
	resp, err := d.client.Post(d.base+"/api/v1/tasks/"+id+"/cancel", "application/json", nil)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (d *daemon) total() (int, error) {
	resp, err := d.client.Get(d.base + "/api/v1/tasks?limit=1")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Total, nil
}

// rssMB reads the child's resident set size from the OS.
func (d *daemon) rssMB() (float64, error) {
	if d.cmd == nil || d.cmd.Process == nil {
		return 0, errors.New("daemon not running")
	}
	pid := d.cmd.Process.Pid
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					break
				}
				kb, err := strconv.ParseFloat(fields[1], 64)
				if err != nil {
					return 0, err
				}
				return kb / 1024, nil
			}
		}
		return 0, errors.New("VmRSS not found in /proc status")
	case "darwin":
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return 0, err
		}
		kb, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		if err != nil {
			return 0, err
		}
		return kb / 1024, nil
	default:
		return 0, fmt.Errorf("RSS sampling unsupported on %s", runtime.GOOS)
	}
}

// sampleRSS samples until the window elapses, persisting each sample so a long
// soak leaves an auditable trace even if the process is interrupted.
func (d *daemon) sampleRSS(window, interval time.Duration, path string) []float64 {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	var series []float64
	f, err := os.Create(path)
	if err != nil {
		f = nil
	}
	if f != nil {
		defer f.Close()
	}
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if v, err := d.rssMB(); err == nil {
			series = append(series, v)
			if f != nil {
				fmt.Fprintf(f, "%s %.1f\n", time.Now().UTC().Format(time.RFC3339), v)
			}
		}
		time.Sleep(interval)
	}
	return series
}

// ---- phases --------------------------------------------------------------

type latencyResult struct {
	samples []float64
	p50     float64
	p99     float64
	max     float64
	pollMs  int
}

func measureCreateLatency(bin, dir string, fx *fixture, n int) (latencyResult, error) {
	res := latencyResult{pollMs: 5}
	if n <= 0 {
		return res, errors.New("latency-samples must be > 0")
	}
	d, err := spawnDaemon(bin, dir)
	if err != nil {
		return res, err
	}
	defer d.stop()
	if _, err := d.waitReady(60 * time.Second); err != nil {
		return res, err
	}
	for i := 0; i < n; i++ {
		start := time.Now()
		id, err := d.create(fmt.Sprintf("%s/fast/%d.bin", fx.base, i))
		if err != nil {
			return res, err
		}
		deadline := time.Now().Add(15 * time.Second)
		got := false
		for time.Now().Before(deadline) {
			t, err := d.getTask(id)
			if err == nil && t.BytesDone > 0 {
				got = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		elapsed := time.Since(start).Seconds() * 1000
		d.cancel(id)
		if !got {
			return res, fmt.Errorf("task %s never transferred a byte within 15s", id)
		}
		res.samples = append(res.samples, elapsed)
	}
	sorted := append([]float64(nil), res.samples...)
	sort.Float64s(sorted)
	res.p50 = percentile(sorted, 50)
	res.p99 = percentile(sorted, 99)
	res.max = sorted[len(sorted)-1]
	return res, nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int((p / 100) * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type residentResult struct {
	d          *daemon
	rssBefore  float64
	readyMs    int64
	restoredMs int64
	restored   int
}

// measureResident fills the daemon with `tasks` resident transfers, then
// restarts it against the same database and times the restore.
func measureResident(bin, dir string, fx *fixture, tasks int) (*residentResult, error) {
	if tasks <= 0 {
		return nil, errors.New("tasks must be > 0")
	}
	d, err := spawnDaemon(bin, dir)
	if err != nil {
		return nil, err
	}
	if _, err := d.waitReady(60 * time.Second); err != nil {
		d.stop()
		return nil, err
	}
	for i := 0; i < tasks; i++ {
		if _, err := d.create(fmt.Sprintf("%s/trickle/%d.bin", fx.base, i)); err != nil {
			d.stop()
			return nil, fmt.Errorf("create task %d: %w", i, err)
		}
	}
	deadline := time.Now().Add(120 * time.Second)
	for {
		total, err := d.total()
		if err == nil && total >= tasks {
			break
		}
		if time.Now().After(deadline) {
			d.stop()
			return nil, fmt.Errorf("daemon did not surface %d tasks (last total=%d)", tasks, total)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(5 * time.Second) // let all dispatch slots settle
	rssBefore, rssErr := d.rssMB()
	if rssErr != nil {
		// Platform without child-RSS access: keep going so the startup phase is
		// still measured; memory metrics become NOT_EXECUTED upstream.
		rssBefore = -1
	}
	d.stop()

	// Cold start against the populated database.
	start := time.Now()
	d2, err := spawnDaemon(bin, dir)
	if err != nil {
		return nil, err
	}
	ready, err := d2.waitReady(120 * time.Second)
	if err != nil {
		d2.stop()
		return nil, err
	}
	_ = start
	restored, restoredMs, err := waitRestored(d2, tasks, 120*time.Second)
	if err != nil {
		d2.stop()
		return nil, err
	}
	return &residentResult{
		d: d2, rssBefore: rssBefore, readyMs: ready.Milliseconds(),
		restoredMs: restoredMs.Milliseconds(), restored: restored,
	}, nil
}

func waitRestored(d *daemon, want int, timeout time.Duration) (int, time.Duration, error) {
	start := time.Now()
	deadline := start.Add(timeout)
	var total int
	for time.Now().Before(deadline) {
		t, err := d.total()
		if err == nil {
			total = t
			if total >= want {
				return total, time.Since(start), nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return total, time.Since(start), fmt.Errorf("only %d/%d tasks visible after %s", total, want, timeout)
}

// ---- small helpers -------------------------------------------------------

// startupMet encodes the docs/12 §5 startup criterion. The healthz timestamp is
// the clock (it already includes Recover, because app.Run restores before it
// starts listening) AND every task must actually be back — a fast listen with
// half the tasks missing is not a pass.
func startupMet(readyMs, restored, tasks int64) bool {
	return readyMs <= startupTarget.Milliseconds() && restored >= tasks
}

func readHost() hostInfo {
	h := hostInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU()}
	if runtime.GOOS == "linux" {
		if raw, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			h.Kernel = strings.TrimSpace(string(raw))
		}
	}
	if out, err := exec.Command("uname", "-a").Output(); err == nil {
		h.UptimeWin = truncate2(strings.TrimSpace(string(out)), 160)
	}
	return h
}

func seriesStats(s []float64) (max, min, last float64) {
	if len(s) == 0 {
		return 0, 0, 0
	}
	max, min = s[0], s[0]
	for _, v := range s {
		if v > max {
			max = v
		}
		if v < min {
			min = v
		}
	}
	return max, min, s[len(s)-1]
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func shortDur(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return d.String()
}

func truncate2(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
