package wechat

import "context"

// PrivilegeHelper is the IPC contract for the separate, elevated helper binary
// (WBS 18 / ADR-0032 decision 1, docs/09 §提权助手). The helper is NOT part of
// the main daemon: it is an independent process with independent privileges,
// distributed as a separate signed installer, and every mutating operation
// requires explicit user authorization.
//
// This interface is intentionally a contract only. Implementations (Windows
// service, macOS privileged helper, polkit helper) live in the isolated plugin
// distribution and are NOT compiled into the main `ud` binary. Keeping the
// contract here lets the main package compile and be tested without any
// system-level side effect.
type PrivilegeHelper interface {
	// InstallCA installs a plugin-scoped root CA used solely by the local MITM
	// proxy. Must prompt for explicit consent and be reversible by UninstallCA.
	InstallCA(ctx context.Context, pem []byte) error
	// UninstallCA removes the CA installed by InstallCA. Must be idempotent.
	UninstallCA(ctx context.Context) error
	// SetSystemProxy points the system proxy at the local MITM endpoint. The
	// daemon must restore the previous value via RestoreSystemProxy on exit.
	SetSystemProxy(ctx context.Context, endpoint string) error
	// RestoreSystemProxy restores the pre-install proxy configuration.
	RestoreSystemProxy(ctx context.Context) error
	// InjectScript injects the extraction script into the target client session.
	// Explicit consent is required per invocation (no silent re-injection).
	InjectScript(ctx context.Context, script []byte) error
	// UpdateRules hot-swaps the signed reverse-engineering rule package.
	// The caller MUST verify Ed25519 signature before invoking (see RulesVerifier).
	UpdateRules(ctx context.Context, signed []byte) error
	// Status reports whether the helper is installed and its current state.
	Status(ctx context.Context) (HelperStatus, error)
}

// HelperStatus is the health/consent snapshot surfaced to the plugin UI.
type HelperStatus struct {
	Installed      bool   `json:"installed"`
	CAActive       bool   `json:"caActive"`
	ProxyActive    bool   `json:"proxyActive"`
	RulesVersion   string `json:"rulesVersion"`
	LastAuthorized int64  `json:"lastAuthorized"`
}

// RulesVerifier validates the detached Ed25519 signature of a rule package
// before any UpdateRules call. It mirrors internal/plugin's governance model
// (ADR-0018 decision 4): a bad signature fails closed.
type RulesVerifier interface {
	Verify(pubKey []byte, payload, sig []byte) error
}

// ConsentGate is the explicit-authorization checkpoint. Every privileged
// operation must pass through it; the main daemon never bypasses it.
type ConsentGate interface {
	Authorize(ctx context.Context, op string) (bool, error)
}

// ComplianceNote records the legal boundary for the distribution package.
// It is embedded in the plugin install UI and the compliance-review artifact.
const ComplianceNote = "仅下载用户有权保存的公开内容；不触碰 DRM、不绕过会员限制、不内置账号共享。" +
	"安装根 CA 与系统代理需用户逐次显式授权；卸载时完整清理且不影响主程序。" +
	"本插件单独分发、默认不安装；视频号 MITM 能力须经法务实名评审后方可启用（当前为发起人代行签批，待补实名复核）。"
