package handler

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeDesignDraftPath(t *testing.T) {
	t.Parallel()
	valid := []string{"index.html", "assets/app.js", "images/logo.svg", "fonts/ui.woff2"}
	for _, input := range valid {
		if _, _, err := normalizeDesignDraftPath(input); err != nil {
			t.Errorf("normalizeDesignDraftPath(%q): %v", input, err)
		}
	}
	invalid := []string{"", "/index.html", "../index.html", "assets/../index.html", ".hidden/app.js", "app.exe", "assets//app.js"}
	for _, input := range invalid {
		if _, _, err := normalizeDesignDraftPath(input); err == nil {
			t.Errorf("normalizeDesignDraftPath(%q) succeeded, want error", input)
		}
	}
}

func TestDesignPreviewTokenIsBoundAndExpires(t *testing.T) {
	t.Parallel()
	h := &Handler{cfg: Config{DesignPreviewSecret: "test-secret"}}
	claims := designPreviewClaims{DraftID: "draft", Revision: "revision", Expires: time.Now().Add(time.Minute).Unix()}
	token := h.signDesignPreview(claims)
	got, ok := h.verifyDesignPreview(token)
	if !ok || got.DraftID != claims.DraftID || got.Revision != claims.Revision {
		t.Fatalf("verifyDesignPreview() = (%+v, %v), want original claims", got, ok)
	}

	replacement := "A"
	if token[len(token)-1:] == replacement {
		replacement = "B"
	}
	tampered := token[:len(token)-1] + replacement
	if _, ok := h.verifyDesignPreview(tampered); ok {
		t.Fatal("verifyDesignPreview accepted a tampered token")
	}

	expired := h.signDesignPreview(designPreviewClaims{DraftID: "draft", Revision: "revision", Expires: time.Now().Add(-time.Second).Unix()})
	if _, ok := h.verifyDesignPreview(expired); ok {
		t.Fatal("verifyDesignPreview accepted an expired token")
	}
}

func TestDesignDraftPreviewCSPAllowsExternalHTTPSResources(t *testing.T) {
	t.Parallel()
	csp := designDraftPreviewCSP("https://multica.example.test")
	for _, directive := range []string{
		"script-src 'self' 'unsafe-inline' https: blob:",
		"style-src 'self' 'unsafe-inline' https:",
		"img-src 'self' https: data: blob:",
		"connect-src https: wss:",
		"frame-ancestors https://multica.example.test",
		"object-src 'none'",
		"form-action 'none'",
		"base-uri 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP missing %q: %s", directive, csp)
		}
	}
	if strings.Contains(csp, "connect-src 'none'") {
		t.Fatalf("CSP still blocks external connections: %s", csp)
	}
}

func TestDesignDraftPreviewAllowsOpaqueSandboxOrigin(t *testing.T) {
	t.Parallel()
	if designPreviewCORSOrigin != "*" {
		t.Fatalf("designPreviewCORSOrigin = %q, want *", designPreviewCORSOrigin)
	}
}
