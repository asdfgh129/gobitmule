package app

import (
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
)

// browserURL turns a listener address into the URL a local browser can reach.
// Wildcard binds are mapped to loopback so auto-open never depends on the
// machine's external interfaces.
func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]", "::1", "[::1]":
		host = "::1"
	}
	if port == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port) + "/#/search"
}

// openBrowser uses the OS URL handler. It is intentionally best-effort: the
// daemon is fully usable via the printed URL if no handler is installed.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func maybeOpenBrowser(opts Options, addr string, loopback bool, log *slog.Logger) {
	if !opts.OpenBrowser || os.Getenv("UD_NO_BROWSER") != "" {
		return
	}
	// Non-loopback listeners require a bearer token that a freshly launched
	// browser does not have; leave the URL to the operator instead.
	if !loopback {
		log.Warn("auto-open skipped for non-loopback listener", "addr", addr)
		return
	}
	url := browserURL(addr)
	if url == "" {
		return
	}
	if err := openBrowser(url); err != nil {
		log.Warn("cannot open browser", "url", url, "error", err)
		return
	}
	log.Info("web console opened", "url", url)
}
