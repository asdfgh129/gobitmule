package wechat

import (
	"context"
	"strings"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

func TestIsPublicArticle(t *testing.T) {
	yes := []string{
		"https://mp.weixin.qq.com/s/abc123",
		"http://mp.weixin.qq.com/s?__biz=xyz",
		"https://MP.WEIXIN.QQ.COM/s/UPPER",
	}
	no := []string{
		"", "not a url",
		"https://weixin.qq.com/",             // not mp.
		"https://v.qq.com/x/cover.html",      // unrelated
		"https://channels.weixin.qq.com/x",   // 视频号 surface, not public article
		"weixin://dl/business/?ticket=abc",   // app deep link
		"ftp://mp.weixin.qq.com/s/x",         // wrong scheme
	}
	for _, raw := range yes {
		if !IsPublicArticle(raw) {
			t.Errorf("should match public article: %q", raw)
		}
	}
	for _, raw := range no {
		if IsPublicArticle(raw) {
			t.Errorf("should NOT match: %q", raw)
		}
	}
}

type fakeDetector struct {
	url   string
	title string
	err   error
}

func (f fakeDetector) Detect(_ context.Context, _ string) (string, string, error) {
	return f.url, f.title, f.err
}

func TestArticleResolverNilDetectorFailsClosed(t *testing.T) {
	r := NewArticleResolver(nil, nil)
	_, err := r.Resolve(core.ResolveContext{Ctx: context.Background()},
		core.Input{Kind: core.InputURL, Raw: "https://mp.weixin.qq.com/s/x"})
	if core.AsError(err).Code != core.E_CAPABILITY_UNAVAILABLE {
		t.Fatalf("nil detector must fail with capability error, got %v", err)
	}
}

func TestArticleResolverSanitizesName(t *testing.T) {
	got := sanitizeName("a/b:c*d?e\"f<g>h|i")
	if strings.ContainsAny(got, `/:*?"<>|`) {
		t.Fatalf("name not sanitized: %q", got)
	}
	if sanitizeName("   ") != "wechat-article" {
		t.Fatal("empty name must get default")
	}
}

func TestComplianceNoteRedLines(t *testing.T) {
	for _, kw := range []string{"不触碰 DRM", "默认不安装", "显式授权", "实名复核"} {
		if !strings.Contains(ComplianceNote, kw) {
			t.Errorf("compliance note missing %q", kw)
		}
	}
}
