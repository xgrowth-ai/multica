package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestLocalUploadsRequireIdentityAndWorkspaceMembership(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCAL_UPLOAD_DIR", dir)
	key := "workspaces/" + testWorkspaceID + "/private.txt"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, key)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key), []byte("private data"), 0600); err != nil {
		t.Fatal(err)
	}
	h := *testHandler
	h.Storage = storage.NewLocalStorageFromEnv()
	outsider := dbfx.User(t, "Outsider", "upload-outsider@example.com")
	for _, tc := range []struct {
		user, path string
		status     int
	}{
		{"", "/uploads/" + key, 404},
		{outsider, "/uploads/" + key, 404},
		{testUserID, "/uploads/" + key, 200},
		{testUserID, "/uploads/", 404},
		{testUserID, "/uploads/workspaces/" + testWorkspaceID + "/", 404},
		{testUserID, "/uploads/design-drafts/" + testWorkspaceID + "/draft/index.html", 404},
		{testUserID, "/uploads/workspaces/" + testWorkspaceID + "/../../design-drafts/x.html", 404},
		{testUserID, "/uploads/users/" + outsider + "/file.txt", 404},
	} {
		req := testutil.WithHeaders(testutil.JSONRequest("GET", tc.path, nil), "X-User-ID", tc.user)
		testutil.Call(t, h.ServeLocalUpload, req).Want(tc.status)
	}
	// The actual router wraps this route in Auth; client identity headers
	// without a valid credential must never reach the storage handler.
	guard := middleware.Auth(h.Queries, nil, nil, nil)(http.HandlerFunc(h.ServeLocalUpload))
	req := testutil.WithHeaders(testutil.JSONRequest("GET", "/uploads/"+key, nil), "X-User-ID", testUserID)
	testutil.Call(t, guard.ServeHTTP, req).Want(401)
	user, err := h.Queries.GetUser(req.Context(), parseUUID(testUserID))
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.issueJWT(user)
	if err != nil {
		t.Fatal(err)
	}
	req = testutil.JSONRequest("GET", "/uploads/"+key, nil)
	req.AddCookie(&http.Cookie{Name: auth.AuthCookieName, Value: token})
	testutil.Call(t, guard.ServeHTTP, req).Want(200)
}

func TestFeishuRejectsExistingAccountOutsideAllowlist(t *testing.T) {
	t.Setenv("VERIFICATION_CODE_DELIVERY", "feishu")
	t.Setenv("FEISHU_VERIFICATION_WEBHOOK_URL", "https://example.test/hook")
	t.Setenv("ALLOWED_EMAILS", "another@example.com")
	h := *testHandler
	h.EmailService = service.NewEmailService()
	testutil.Call(t, h.SendCode, testutil.JSONRequest("POST", "/auth/send-code", map[string]string{"email": handlerTestEmail})).Want(403)
	testutil.Call(t, h.VerifyCode, testutil.JSONRequest("POST", "/auth/verify-code", map[string]string{"email": handlerTestEmail, "code": "123456"})).Want(403)
}

func TestLocalAvatarUsesSignedEndpoint(t *testing.T) {
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	h := *testHandler
	h.Storage = storage.NewLocalStorageFromEnv()
	key := "users/" + testUserID + "/avatar.png"
	resolved := h.resolveAvatarURL("/uploads/" + key)
	if resolved == "/uploads/"+key || resolved == "" {
		t.Fatalf("avatar did not use signed URL: %q", resolved)
	}
}
