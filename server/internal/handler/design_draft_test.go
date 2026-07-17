package handler

import (
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
