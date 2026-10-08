package handler

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestNormalizeLoginEmail(t *testing.T) {
	for _, raw := range []string{"", "sample@email.tst<esi:include/>", "/etc/shells", "x\n@example.com", "Name <a@example.com>", "a@example.com,b@example.com", strings.Repeat("a", 65) + "@example.com", "a@" + strings.Repeat("a", 250) + ".com"} {
		if _, ok := normalizeLoginEmail(raw); ok {
			t.Errorf("accepted invalid email %q", raw)
		}
	}
	if got, ok := normalizeLoginEmail("  Alice+tag@Example.com  "); !ok || got != "alice+tag@example.com" {
		t.Fatalf("normalized email = %q, %v", got, ok)
	}
}

func TestLoginRejectsMalformedAndOversizedRequests(t *testing.T) {
	h := &Handler{}
	for _, body := range []string{`{"email":"/etc/passwd"}`, `{"email":"a@example.com"} {}`, `{"email":"` + strings.Repeat("x", 4100) + `"}`} {
		testutil.Call(t, h.SendCode, testutil.JSONRequest("POST", "/auth/send-code", body)).Want(http.StatusBadRequest)
	}
	testutil.Call(t, h.VerifyCode, testutil.JSONRequest("POST", "/auth/verify-code", map[string]string{"email": "a@example.com", "code": "123"})).Want(http.StatusBadRequest)
}

func securityCode(t *testing.T, email, code string, over testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{"email": email, "code": code, "expires_at": time.Now().Add(10 * time.Minute)}
	for k, v := range over {
		cols[k] = v
	}
	return dbfx.Insert(t, "verification_code", cols)
}

func concurrentCodeRequests(t *testing.T, h http.HandlerFunc, route, email, code string, n int) map[int]int {
	t.Helper()
	results := make(chan int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Go(func() {
			<-start
			req := testutil.JSONRequest("POST", route, map[string]string{"email": email, "code": code})
			results <- testutil.Call(t, h, req).Code
		})
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	return counts
}

func TestVerifyCodeConcurrentConsumption(t *testing.T) {
	securityCode(t, handlerTestEmail, "123456", nil)
	counts := concurrentCodeRequests(t, testHandler.VerifyCode, "/auth/verify-code", handlerTestEmail, "123456", 20)
	if counts[200] != 1 || counts[400] != 19 {
		t.Fatalf("concurrent consumption statuses: %v", counts)
	}
}

func TestVerifyCodeConcurrentAttempts(t *testing.T) {
	id := securityCode(t, handlerTestEmail, "123456", nil)
	counts := concurrentCodeRequests(t, testHandler.VerifyCode, "/auth/verify-code", handlerTestEmail, "654321", 20)
	if counts[400] != 20 {
		t.Fatalf("wrong-code statuses: %v", counts)
	}
	var attempts int
	dbfx.QueryRow(t, `SELECT attempts FROM verification_code WHERE id=$1`, id).Scan(&attempts)
	if attempts != 5 {
		t.Fatalf("attempts=%d, want 5", attempts)
	}
	testutil.Call(t, testHandler.VerifyCode, testutil.JSONRequest("POST", "/auth/verify-code", map[string]string{"email": handlerTestEmail, "code": "123456"})).Want(400)
}

func TestVerifyCodeDoesNotFallBackToOlderCode(t *testing.T) {
	for _, over := range []testutil.Cols{{"used": true}, {"attempts": 5}, {"expires_at": time.Now().Add(-time.Minute)}} {
		t.Run(fmt.Sprint(over), func(t *testing.T) {
			securityCode(t, handlerTestEmail, "123456", testutil.Cols{"created_at": time.Now().Add(-2 * time.Minute)})
			securityCode(t, handlerTestEmail, "654321", over)
			testutil.Call(t, testHandler.VerifyCode, testutil.JSONRequest("POST", "/auth/verify-code", map[string]string{"email": handlerTestEmail, "code": "123456"})).Want(400)
		})
	}
}

func TestSendCodeConcurrentIssuance(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	email := "concurrent-issuance@example.com"
	dbfx.Cleanup(t, `DELETE FROM verification_code WHERE email=$1`, email)
	counts := concurrentCodeRequests(t, testHandler.SendCode, "/auth/send-code", email, "", 20)
	if counts[200] != 1 || counts[429] != 19 {
		t.Fatalf("concurrent issuance statuses: %v", counts)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM verification_code WHERE email=$1`, email); n != 1 {
		t.Fatalf("created %d codes, want 1", n)
	}
}

func TestSendCodeInvalidatesOlderCodes(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	email := "replace-code@example.com"
	old := securityCode(t, email, "123456", testutil.Cols{"created_at": time.Now().Add(-2 * time.Minute)})
	dbfx.Cleanup(t, `DELETE FROM verification_code WHERE email=$1`, email)
	testutil.Call(t, testHandler.SendCode, testutil.JSONRequest("POST", "/auth/send-code", map[string]string{"email": email})).Want(200)
	var used bool
	dbfx.QueryRow(t, `SELECT used FROM verification_code WHERE id=$1`, old).Scan(&used)
	if !used {
		t.Fatal("older code remains usable")
	}
}

func TestSendCodeProductionRequiresDelivery(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	h := *testHandler
	h.EmailService = &service.EmailService{}
	email := "no-delivery@example.com"
	testutil.Call(t, h.SendCode, testutil.JSONRequest("POST", "/auth/send-code", map[string]string{"email": email})).Want(503)
	if n := dbfx.Count(t, `SELECT count(*) FROM verification_code WHERE email=$1`, email); n != 0 {
		t.Fatalf("stored %d undeliverable codes", n)
	}
}
