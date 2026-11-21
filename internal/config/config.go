// Package config implements the configuration precedence of docs/13 §7:
// built-in defaults < config file < environment < CLI flags < API runtime patch.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type GlobalConfig struct {
	MaxConcurrentTasks    int    `yaml:"maxConcurrentTasks" json:"maxConcurrentTasks"`
	MaxDownloadRate       string `yaml:"maxDownloadRate" json:"maxDownloadRate"`
	MaxUploadRate         string `yaml:"maxUploadRate" json:"maxUploadRate"`
	MaxConnectionsPerTask int    `yaml:"maxConnectionsPerTask" json:"maxConnectionsPerTask"`
	ChunkSize             string `yaml:"chunkSize" json:"chunkSize"`
	DiskFlushInterval     string `yaml:"diskFlushInterval" json:"diskFlushInterval"`
}

type SecurityConfig struct {
	AllowedSchemes       []string `yaml:"allowedSchemes" json:"allowedSchemes"`
	AllowedPorts         []int    `yaml:"allowedPorts" json:"allowedPorts"`
	MaxRedirects         int      `yaml:"maxRedirects" json:"maxRedirects"`
	MaxMetalinkMirrors   int      `yaml:"maxMetalinkMirrors" json:"maxMetalinkMirrors"`
	MaxXMLBytes          int64    `yaml:"maxXMLBytes" json:"maxXMLBytes"`
	BlockPrivateNetworks bool     `yaml:"blockPrivateNetworks" json:"blockPrivateNetworks"`
	AllowLoopbackForDev  bool     `yaml:"allowLoopbackForDev" json:"allowLoopbackForDev"`
}

// BTConfig is the LIVE BitTorrent upload-etiquette configuration (WBS 7 +
// ADR-0021 path A). It used to be a reserved group that was parsed but
// ignored; the daemon now maps it onto bttransport.SeedingPolicy. Global byte
// rates deliberately stay out of this struct: scheduler.Limiter is the only
// rate authority (docs/04 §5.3, check_arch.py guard 4).
type BTConfig struct {
	PeerID          string  `yaml:"peerID" json:"peerID"`
	EnableDHT       bool    `yaml:"enableDHT" json:"enableDHT"`
	EnablePEX       bool    `yaml:"enablePEX" json:"enablePEX"`
	SeedEnabled     bool    `yaml:"seedEnabled" json:"seedEnabled"`
	SeedRatio       float64 `yaml:"seedRatio" json:"seedRatio"`
	SeedTimeMinutes int     `yaml:"seedTimeMinutes" json:"seedTimeMinutes"`
	MaxUploadSlots  int     `yaml:"maxUploadSlots" json:"maxUploadSlots"`
	EnableUPnP      bool    `yaml:"enableUPnP" json:"enableUPnP"`
	ListenPort      int     `yaml:"listenPort" json:"listenPort"`
	ListenHost      string  `yaml:"listenHost" json:"listenHost"`
}

type ED2KConfig struct {
	Obfuscation bool `yaml:"obfuscation" json:"obfuscation"`
	SecureIdent bool `yaml:"secureIdent" json:"secureIdent"`
	TCPPort     int  `yaml:"tcpPort" json:"tcpPort"`
	UDPPort     int  `yaml:"udpPort" json:"udpPort"`
}

// PluginsConfig is the WBS 10 rule-channel configuration (ADR-0014, docs/13 §7).
// Dir is empty by default: no plugin Resolver is registered at all until the
// operator explicitly points ud at a rule directory.
type PluginsConfig struct {
	VerifySignature bool     `yaml:"verifySignature" json:"verifySignature"`
	Runtime         string   `yaml:"runtime" json:"runtime"`
	Dir             string   `yaml:"dir" json:"dir"`
	TrustedKeys     []string `yaml:"trustedKeys" json:"trustedKeys"`
	Insecure        bool     `yaml:"insecure" json:"insecure"`
	ReloadInterval  string   `yaml:"reloadInterval" json:"reloadInterval"`
}

// Validate fails closed: disabling signature checks requires an explicit,
// local-dev-only opt-in, and an unimplemented runtime must never be accepted
// as "no plugins".
func (p PluginsConfig) Validate() error {
	if p.Runtime != "" && p.Runtime != "goja" {
		return fmt.Errorf("unsupported runtime %q (only goja is implemented; wasm is a future upgrade path)", p.Runtime)
	}
	if p.ReloadInterval != "" {
		d, err := time.ParseDuration(p.ReloadInterval)
		if err != nil || d < time.Second || d > 24*time.Hour {
			return fmt.Errorf("reloadInterval %q must be a duration within 1s..24h", p.ReloadInterval)
		}
	}
	if !p.VerifySignature && !p.Insecure {
		return errors.New("verifySignature=false requires insecure=true (explicit local-dev opt-in)")
	}
	return nil
}

// PostProcessConfig is the optional desktop ffmpeg Transcode plugin
// (ADR-0011 / ADR-0012). It is OFF by default: the zero-sidecar baseline must
// stay true until an operator explicitly points ud at a local LGPL/GPL build.
// Missing or unsuitable binaries degrade to E_CAPABILITY_UNAVAILABLE and never
// block downloads.
type PostProcessConfig struct {
	FFmpegEnabled       bool   `yaml:"ffmpegEnabled" json:"ffmpegEnabled"`
	FFmpegBinary        string `yaml:"ffmpegBinary" json:"ffmpegBinary"`
	FFmpegThreads       int    `yaml:"ffmpegThreads" json:"ffmpegThreads"`
	FFmpegMaxConcurrent int    `yaml:"ffmpegMaxConcurrent" json:"ffmpegMaxConcurrent"`
	FFmpegTimeout       string `yaml:"ffmpegTimeout" json:"ffmpegTimeout"`
}

func (p PostProcessConfig) EffectiveTimeout() time.Duration {
	if strings.TrimSpace(p.FFmpegTimeout) == "" {
		return 30 * time.Minute
	}
	d, err := time.ParseDuration(p.FFmpegTimeout)
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

func (p PostProcessConfig) Validate() error {
	if !p.FFmpegEnabled {
		return nil
	}
	if strings.TrimSpace(p.FFmpegBinary) == "" {
		return errors.New("postProcess.ffmpegBinary is required when ffmpegEnabled=true")
	}
	if strings.ContainsAny(p.FFmpegBinary, "\x00\r\n") {
		return errors.New("postProcess.ffmpegBinary contains control characters")
	}
	if p.FFmpegThreads < 1 || p.FFmpegThreads > 64 {
		return errors.New("postProcess.ffmpegThreads must be within 1..64")
	}
	if p.FFmpegMaxConcurrent < 1 || p.FFmpegMaxConcurrent > 16 {
		return errors.New("postProcess.ffmpegMaxConcurrent must be within 1..16")
	}
	d, err := time.ParseDuration(p.FFmpegTimeout)
	if err != nil || d < time.Minute || d > 24*time.Hour {
		return errors.New("postProcess.ffmpegTimeout must be within 1m..24h")
	}
	return nil
}

// SourcesConfig gates optional content-source resolvers. yt-dlp is a sidecar
// that only emits JSON (ADR-0010); it stays OFF by default so the zero-sidecar
// baseline and the P0/P1 capability surface are unchanged.
type SourcesConfig struct {
	YTDLP    YTDLPConfig    `yaml:"ytdlp" json:"ytdlp"`
	Lux      LuxConfig      `yaml:"lux" json:"lux"`
	Telegram TelegramConfig `yaml:"telegram" json:"telegram"`
}

// TelegramConfig gates the MTProto transport (WBS 15, ADR-0029). apiId /
// apiHash are the Telegram APP credentials (an app identifier, not a user
// secret); the USER session blob travels through the credential store
// (FR-SRC-06). Disabled by default: an unconfigured daemon must not accept
// telegram links.
type TelegramConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	APIID   int    `yaml:"apiId" json:"apiId"`
	APIHash string `yaml:"apiHash" json:"-"`
	Timeout string `yaml:"timeout" json:"timeout"`
}

// EffectiveTimeout returns the MTProto operation bound, falling back to 10m
// (large documents need minutes, not seconds).
func (t TelegramConfig) EffectiveTimeout() time.Duration {
	if strings.TrimSpace(t.Timeout) == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(t.Timeout)
	if err != nil || d <= 0 {
		return 10 * time.Minute
	}
	return d
}

// LuxConfig gates the in-process Chinese-site parser (ADR-0010 decision 1 /
// ADR-0026). It is pure Go with no sidecar; disabled by default so enabling
// it is an explicit operator decision (the extraction makes same-site network
// requests for the page itself).
type LuxConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Timeout string `yaml:"timeout" json:"timeout"`
}

// EffectiveTimeout returns the extraction bound, falling back to 60s.
func (l LuxConfig) EffectiveTimeout() time.Duration {
	if strings.TrimSpace(l.Timeout) == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(l.Timeout)
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

type YTDLPConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Binary   string `yaml:"binary" json:"binary"`
	Timeout  string `yaml:"timeout" json:"timeout"`
	MaxBytes int64  `yaml:"maxBytes" json:"maxBytes"`
}

func (y YTDLPConfig) EffectiveTimeout() time.Duration {
	if strings.TrimSpace(y.Timeout) == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(y.Timeout)
	if err != nil || d <= 0 {
		return 2 * time.Minute
	}
	return d
}

func (y YTDLPConfig) EffectiveMaxBytes() int64 {
	if y.MaxBytes > 0 {
		return y.MaxBytes
	}
	return 16 << 20
}

func (s SourcesConfig) Validate() error {
	if tg := s.Telegram; tg.Enabled {
		if tg.APIID <= 0 || tg.APIID > (1<<31)-1 {
			return fmt.Errorf("sources.telegram.apiId must be within 1..2^31-1 when enabled")
		}
		if strings.TrimSpace(tg.APIHash) == "" {
			return fmt.Errorf("sources.telegram.apiHash is required when enabled")
		}
		if strings.ContainsAny(tg.APIHash, "\x00\r\n") {
			return fmt.Errorf("sources.telegram.apiHash contains control characters")
		}
		if tg.Timeout != "" {
			d, err := time.ParseDuration(tg.Timeout)
			if err != nil || d < 10*time.Second || d > 30*time.Minute {
				return fmt.Errorf("sources.telegram.timeout %q must be a duration within 10s..30m", tg.Timeout)
			}
		}
	}
	if l := s.Lux; l.Enabled {
		if l.Timeout != "" {
			d, err := time.ParseDuration(l.Timeout)
			if err != nil || d < time.Second || d > 5*time.Minute {
				return fmt.Errorf("sources.lux.timeout %q must be a duration within 1s..5m", l.Timeout)
			}
		}
	}
	if !s.YTDLP.Enabled {
		return nil
	}
	if strings.TrimSpace(s.YTDLP.Binary) == "" {
		return errors.New("sources.ytdlp.binary is required when enabled")
	}
	if strings.ContainsAny(s.YTDLP.Binary, "\x00\r\n") {
		return errors.New("sources.ytdlp.binary contains control characters")
	}
	if s.YTDLP.Timeout != "" {
		d, err := time.ParseDuration(s.YTDLP.Timeout)
		if err != nil || d < time.Second || d > 30*time.Minute {
			return errors.New("sources.ytdlp.timeout must be within 1s..30m")
		}
	}
	if s.YTDLP.MaxBytes < 0 || s.YTDLP.MaxBytes > 1<<30 {
		return errors.New("sources.ytdlp.maxBytes must be within 0..1GiB")
	}
	return nil
}

type TelemetryConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// EventsConfig bounds audit-log growth (docs/06 §7: "保留策略可配（默认 7 天 /
// 10 万条），定期清理"). Both limits are enforced independently, so a burst of
// events can never outgrow maxRows and an idle daemon can never keep ancient
// rows forever. At least one limit must be non-zero: an unbounded `events` table
// is a disk-filling bug, so the daemon fails closed instead of accepting it.
type EventsConfig struct {
	// RetentionDays deletes events older than this many days (0 = no age limit).
	RetentionDays int `yaml:"retentionDays" json:"retentionDays"`
	// MaxRows keeps only the newest N rows (0 = no row cap).
	MaxRows int `yaml:"maxRows" json:"maxRows"`
	// PruneInterval is the janitor cadence. Empty = default (1h).
	PruneInterval string `yaml:"pruneInterval" json:"pruneInterval"`
}

// EffectivePruneInterval returns the janitor cadence, falling back to 1h.
func (e EventsConfig) EffectivePruneInterval() time.Duration {
	if e.PruneInterval == "" {
		return time.Hour
	}
	d, err := time.ParseDuration(e.PruneInterval)
	if err != nil || d <= 0 {
		return time.Hour
	}
	return d
}

// CutoffMillis converts the retention window into an absolute timestamp. It
// returns 0 when there is no age-based limit, which PruneEvents reads as
// "skip the age sweep".
func (e EventsConfig) CutoffMillis(now time.Time) int64 {
	if e.RetentionDays <= 0 {
		return 0
	}
	return now.Add(-time.Duration(e.RetentionDays) * 24 * time.Hour).UnixMilli()
}

func (e EventsConfig) Validate() error {
	if e.RetentionDays < 0 {
		return errors.New("retentionDays must be >= 0")
	}
	if e.MaxRows < 0 {
		return errors.New("maxRows must be >= 0")
	}
	if e.RetentionDays == 0 && e.MaxRows == 0 {
		return errors.New("requires retentionDays or maxRows > 0 (unbounded audit log)")
	}
	if e.PruneInterval != "" {
		d, err := time.ParseDuration(e.PruneInterval)
		if err != nil {
			return fmt.Errorf("pruneInterval: %w", err)
		}
		if d < time.Minute || d > 24*time.Hour {
			return errors.New("pruneInterval must be within 1m..24h")
		}
	}
	return nil
}

// Config is the full on-disk schema (docs/13 §7). Only the P0-relevant fields
// are consumed today; the rest are documented reservations.
type Config struct {
	Listen      string            `yaml:"listen" json:"listen"`
	DownloadDir string            `yaml:"downloadDir" json:"downloadDir"`
	TempSuffix  string            `yaml:"tempSuffix" json:"tempSuffix"`
	LogLevel    string            `yaml:"logLevel" json:"logLevel"`
	RemoteToken string            `yaml:"remoteToken" json:"-"`
	Global      GlobalConfig      `yaml:"global" json:"global"`
	Security    SecurityConfig    `yaml:"security" json:"security"`
	BT          BTConfig          `yaml:"bt" json:"bt"`
	ED2K        ED2KConfig        `yaml:"ed2k" json:"ed2k"`
	Plugins     PluginsConfig     `yaml:"plugins" json:"plugins"`
	Events      EventsConfig      `yaml:"events" json:"events"`
	PostProcess PostProcessConfig `yaml:"postProcess" json:"postProcess"`
	Sources     SourcesConfig     `yaml:"sources" json:"sources"`
	Telemetry   TelemetryConfig   `yaml:"telemetry" json:"telemetry"`
}

// Defaults matches config.example.yaml and docs/13 §7.
func Defaults() Config {
	return Config{
		Listen:      "127.0.0.1:0",
		DownloadDir: "~/Downloads/ud",
		TempSuffix:  ".part",
		LogLevel:    "info",
		Global: GlobalConfig{
			MaxConcurrentTasks:    5,
			MaxDownloadRate:       "8MiB/s",
			MaxUploadRate:         "2MiB/s",
			MaxConnectionsPerTask: 16,
			ChunkSize:             "4MiB",
			DiskFlushInterval:     "500ms",
		},
		Security: SecurityConfig{
			AllowedSchemes:       []string{"http", "https", "ftp", "ftps", "sftp"},
			AllowedPorts:         []int{80, 443, 21, 22, 990},
			MaxRedirects:         3,
			MaxMetalinkMirrors:   32,
			MaxXMLBytes:          4 << 20,
			BlockPrivateNetworks: true,
		},
		BT: BTConfig{
			PeerID: "-UD0000-", EnableDHT: true, EnablePEX: true,
			SeedEnabled: true, SeedRatio: 1.0, SeedTimeMinutes: 60,
			MaxUploadSlots: 8, EnableUPnP: true, ListenPort: 0,
		},
		ED2K: ED2KConfig{Obfuscation: true, SecureIdent: true, TCPPort: 4662, UDPPort: 4672},
		Plugins: PluginsConfig{
			VerifySignature: true, Runtime: "goja", ReloadInterval: "30s",
		},
		// docs/06 §7 defaults: 7 days / 100k rows, swept hourly.
		Events: EventsConfig{RetentionDays: 7, MaxRows: 100_000, PruneInterval: "1h"},
		PostProcess: PostProcessConfig{
			FFmpegEnabled: false, FFmpegBinary: "ffmpeg",
			FFmpegThreads: 2, FFmpegMaxConcurrent: 1, FFmpegTimeout: "30m",
		},
		Sources: SourcesConfig{YTDLP: YTDLPConfig{
			Binary: "yt-dlp", Timeout: "2m", MaxBytes: 16 << 20,
		}},
		Telemetry: TelemetryConfig{Enabled: false},
	}
}

// Load overlays a YAML file on top of Defaults, then applies UD_* env overrides.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	cfg.DownloadDir = ExpandHome(cfg.DownloadDir)
	return cfg, nil
}

func applyEnv(c *Config) {
	set := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set("UD_LISTEN", &c.Listen)
	set("UD_DIR", &c.DownloadDir)
	set("UD_LOG_LEVEL", &c.LogLevel)
	set("UD_REMOTE_TOKEN", &c.RemoteToken)
	set("UD_PLUGINS_DIR", &c.Plugins.Dir)
	set("UD_PLUGINS_RUNTIME", &c.Plugins.Runtime)
	set("UD_PLUGINS_RELOAD_INTERVAL", &c.Plugins.ReloadInterval)
	set("UD_YTDLP_BINARY", &c.Sources.YTDLP.Binary)
	set("UD_YTDLP_TIMEOUT", &c.Sources.YTDLP.Timeout)
	if v := os.Getenv("UD_YTDLP_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Sources.YTDLP.Enabled = b
		}
	}
	set("UD_FFMPEG_BINARY", &c.PostProcess.FFmpegBinary)
	set("UD_FFMPEG_TIMEOUT", &c.PostProcess.FFmpegTimeout)
	if v := os.Getenv("UD_FFMPEG_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.PostProcess.FFmpegEnabled = b
		}
	}
	if v := os.Getenv("UD_FFMPEG_THREADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PostProcess.FFmpegThreads = n
		}
	}
	if v := os.Getenv("UD_FFMPEG_MAX_CONCURRENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.PostProcess.FFmpegMaxConcurrent = n
		}
	}
	// ADR-0021 path A: bt.* keys are live now. UD_BT_* overrides let an
	// operator disable or bound seeding without editing the config file.
	set("UD_BT_PEER_ID", &c.BT.PeerID)
	set("UD_BT_LISTEN_HOST", &c.BT.ListenHost)
	if v := os.Getenv("UD_BT_SEED_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.BT.SeedEnabled = b
		}
	}
	if v := os.Getenv("UD_BT_SEED_RATIO"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.BT.SeedRatio = f
		}
	}
	if v := os.Getenv("UD_BT_SEED_TIME_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.BT.SeedTimeMinutes = n
		}
	}
	if v := os.Getenv("UD_BT_UPLOAD_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.BT.MaxUploadSlots = n
		}
	}
	if v := os.Getenv("UD_BT_ENABLE_UPNP"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.BT.EnableUPnP = b
		}
	}
	if v := os.Getenv("UD_BT_LISTEN_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.BT.ListenPort = n
		}
	}
	if v := os.Getenv("UD_PLUGIN_KEYS"); v != "" {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				c.Plugins.TrustedKeys = append(c.Plugins.TrustedKeys, k)
			}
		}
	}
	if v := os.Getenv("UD_PLUGIN_INSECURE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Plugins.Insecure = b
		}
	}
}

// Validate rejects values that would silently break the single scheduler or SSRF guard.
func (c Config) Validate() error {
	if c.Global.MaxConcurrentTasks < 1 {
		return errors.New("global.maxConcurrentTasks must be >= 1")
	}
	if c.Global.MaxConnectionsPerTask < 1 {
		return errors.New("global.maxConnectionsPerTask must be >= 1")
	}
	if _, err := ParseRate(c.Global.MaxDownloadRate); err != nil {
		return fmt.Errorf("global.maxDownloadRate: %w", err)
	}
	if _, err := ParseRate(c.Global.MaxUploadRate); err != nil {
		return fmt.Errorf("global.maxUploadRate: %w", err)
	}
	if _, err := ParseSize(c.Global.ChunkSize); err != nil {
		return fmt.Errorf("global.chunkSize: %w", err)
	}
	if c.Security.MaxRedirects < 0 || c.Security.MaxRedirects > 10 {
		return errors.New("security.maxRedirects must be within 0..10")
	}
	if err := c.Sources.Validate(); err != nil {
		return fmt.Errorf("sources: %w", err)
	}
	if err := c.Plugins.Validate(); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	if err := c.Events.Validate(); err != nil {
		return fmt.Errorf("events: %w", err)
	}
	if err := c.PostProcess.Validate(); err != nil {
		return fmt.Errorf("postProcess: %w", err)
	}
	// ADR-0021 path A: a bounded seeding obligation must always be able to
	// terminate. Both targets being zero with seedEnabled=true would seed
	// forever, so fail closed. Private torrents ignore this policy entirely.
	if c.BT.SeedEnabled {
		if c.BT.SeedRatio < 0 {
			return errors.New("bt.seedRatio must be >= 0")
		}
		if c.BT.SeedTimeMinutes < 0 {
			return errors.New("bt.seedTimeMinutes must be >= 0")
		}
		if c.BT.SeedRatio == 0 && c.BT.SeedTimeMinutes == 0 {
			return errors.New("bt.seedEnabled requires seedRatio or seedTimeMinutes > 0 (bounded seeding)")
		}
		if c.BT.MaxUploadSlots < 1 {
			return errors.New("bt.maxUploadSlots must be >= 1")
		}
		if c.BT.ListenPort < 0 || c.BT.ListenPort > 65535 {
			return errors.New("bt.listenPort must be within 0..65535")
		}
	}
	if c.TempSuffix == "" || strings.ContainsAny(c.TempSuffix, "/\\") {
		return errors.New("tempSuffix must be a simple suffix like .part")
	}
	if !isLoopbackListen(c.Listen) {
		// Unsigned rule packs are a local development escape hatch only; a
		// remotely reachable daemon must never accept them (ADR-0014 §7).
		if c.Plugins.Insecure {
			return errors.New("plugins.insecure requires loopback-only listen")
		}
		// docs/07 §1 / ADR-0016: remote control plane must be explicitly
		// enabled AND authenticated. Never fall back to an unauthenticated
		// 0.0.0.0 listener just because a token was not supplied.
		if c.RemoteToken == "" {
			return errors.New("non-loopback listen requires remoteToken (docs/07 §1 / ADR-0016)")
		}
		if c.Security.AllowLoopbackForDev {
			return errors.New("security.allowLoopbackForDev requires loopback-only listen")
		}
	}
	return nil
}

func isLoopbackListen(listen string) bool {
	host, _, err := splitHostPort(listen)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == ""
}

func splitHostPort(listen string) (string, string, error) {
	i := strings.LastIndex(listen, ":")
	if i < 0 {
		return "", "", errors.New("listen must be host:port")
	}
	return listen[:i], listen[i+1:], nil
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

// ParseRate parses "8MiB/s" / "512KiB/s" / "0" (unlimited) into bytes per second.
func ParseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || strings.EqualFold(s, "unlimited") {
		return 0, nil
	}
	s = strings.TrimSuffix(strings.ToLower(s), "/s")
	return parseUnit(s, "rate")
}

// ParseSize parses "4MiB" / "512KiB" / "1024" into bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	return parseUnit(strings.ToLower(s), "size")
}

func parseUnit(s, what string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{"gib", 1 << 30}, {"gb", 1e9}, {"mib", 1 << 20}, {"mb", 1e6},
		{"kib", 1 << 10}, {"kb", 1e3}, {"b", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suffix)), 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid %s %q", what, s)
			}
			return int64(n * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid %s %q", what, s)
	}
	return int64(n), nil
}

// Manager adds runtime PATCH support (docs/07 §2 `PATCH /config`) with a single
// writer. onChange lets the Limiter and SSRF Guard react without reading config
// themselves — keeping the "one authority" rule intact.
type Manager struct {
	mu       sync.RWMutex
	cfg      Config
	onChange func(Config)
}

func NewManager(c Config, onChange func(Config)) *Manager {
	return &Manager{cfg: c, onChange: onChange}
}

// SetOnChange wires the callback after the Limiter/SSRFGuard have been built
// (they are needed to construct the callback, hence the two-step setup).
func (m *Manager) SetOnChange(f func(Config)) {
	m.mu.Lock()
	m.onChange = f
	m.mu.Unlock()
}

func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Patch accepts flat dotted keys, e.g. {"global.maxDownloadRate": "2MiB/s"}.
// The change callback runs AFTER the lock is released so it may safely call Get().
func (m *Manager) Patch(kv map[string]any) (Config, error) {
	next, cb, err := m.patchLocked(kv)
	if err != nil {
		return m.Get(), err
	}
	if cb != nil {
		cb(next)
	}
	return next, nil
}

func (m *Manager) patchLocked(kv map[string]any) (Config, func(Config), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.cfg
	for k, v := range kv {
		s := fmt.Sprint(v)
		switch k {
		case "global.maxDownloadRate":
			if _, err := ParseRate(s); err != nil {
				return m.cfg, nil, err
			}
			next.Global.MaxDownloadRate = s
		case "global.maxUploadRate":
			if _, err := ParseRate(s); err != nil {
				return m.cfg, nil, err
			}
			next.Global.MaxUploadRate = s
		case "global.maxConcurrentTasks":
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return m.cfg, nil, fmt.Errorf("invalid %s", k)
			}
			next.Global.MaxConcurrentTasks = n
		case "global.maxConnectionsPerTask":
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return m.cfg, nil, fmt.Errorf("invalid %s", k)
			}
			next.Global.MaxConnectionsPerTask = n
		case "global.chunkSize":
			if _, err := ParseSize(s); err != nil {
				return m.cfg, nil, err
			}
			next.Global.ChunkSize = s
		case "security.blockPrivateNetworks":
			b, err := strconv.ParseBool(s)
			if err != nil {
				return m.cfg, nil, fmt.Errorf("invalid %s", k)
			}
			next.Security.BlockPrivateNetworks = b
		default:
			return m.cfg, nil, fmt.Errorf("unsupported config key %q", k)
		}
	}
	if err := next.Validate(); err != nil {
		return m.cfg, nil, err
	}
	m.cfg = next
	return next, m.onChange, nil
}
