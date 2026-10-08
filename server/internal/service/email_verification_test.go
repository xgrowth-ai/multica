package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestVerificationCodeProductionNeverLogsCode(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	s := &EmailService{}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	err = s.SendVerificationCode(context.Background(), "allowed@example.com", "123456")
	os.Stdout = original
	writer.Close()
	logged, readErr := io.ReadAll(reader)
	reader.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err == nil || len(logged) != 0 {
		t.Fatalf("missing delivery should fail without logging; err=%v bytes=%d", err, len(logged))
	}
}

func TestFeishuVerificationDelivery(t *testing.T) {
	for _, result := range []string{`{"code":0}`, `{"StatusCode":0}`, `{"code":1}`, `{}`, `{"code":1,"StatusCode":0}`} {
		t.Run(result, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid webhook request")
				}
				var payload struct {
					MsgType string `json:"msg_type"`
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.MsgType != "text" || !strings.Contains(payload.Content.Text, "allowed@example.com") || !strings.Contains(payload.Content.Text, "123456") {
					t.Error("invalid verification notification")
				}
				io.WriteString(w, result)
			}))
			defer server.Close()
			s := &EmailService{verificationDelivery: "feishu", verificationWebhook: server.URL, verificationAllowedEmails: map[string]struct{}{"allowed@example.com": {}}, verificationHTTPClient: server.Client()}
			err := s.SendVerificationCode(context.Background(), "allowed@example.com", "123456")
			wantSuccess := result == `{"code":0}` || result == `{"StatusCode":0}`
			if (err == nil) != wantSuccess {
				t.Fatalf("success=%v, want %v", err == nil, wantSuccess)
			}
			if requests.Load() != 1 {
				t.Fatalf("sent %d requests", requests.Load())
			}
			if err := s.SendVerificationCode(context.Background(), "outsider@example.com", "123456"); err == nil {
				t.Fatal("unlisted email allowed")
			}
			if requests.Load() != 1 {
				t.Fatal("unlisted email reached webhook")
			}
		})
	}
}

func TestFeishuVerificationDoesNotFollowRedirects(t *testing.T) {
	var deliveries atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { deliveries.Add(1); io.WriteString(w, `{"code":0}`) }))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	t.Setenv("VERIFICATION_CODE_DELIVERY", "feishu")
	t.Setenv("FEISHU_VERIFICATION_WEBHOOK_URL", source.URL)
	t.Setenv("ALLOWED_EMAILS", "allowed@example.com")
	s := NewEmailService()
	s.verificationHTTPClient.Transport = source.Client().Transport
	if s.SendVerificationCode(context.Background(), "allowed@example.com", "123456") == nil {
		t.Fatal("redirect accepted")
	}
	if deliveries.Load() != 0 {
		t.Fatal("verification credential followed a redirect")
	}
}

type verificationTransportFunc func(*http.Request) (*http.Response, error)

func (f verificationTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFeishuVerificationErrorsRedactWebhook(t *testing.T) {
	secretURL := "https://example.test/secret-webhook-path"
	s := &EmailService{verificationDelivery: "feishu", verificationWebhook: secretURL, verificationAllowedEmails: map[string]struct{}{"allowed@example.com": {}}, verificationHTTPClient: &http.Client{Transport: verificationTransportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(secretURL) })}}
	err := s.SendVerificationCode(context.Background(), "allowed@example.com", "123456")
	if err == nil || strings.Contains(err.Error(), "secret-webhook-path") || strings.Contains(err.Error(), "123456") {
		t.Fatal("delivery error exposed a credential")
	}
}

func TestFeishuVerificationConfigurationFailsClosed(t *testing.T) {
	for _, s := range []*EmailService{
		{verificationDelivery: "feishu", verificationWebhook: "https://example.test/hook"},
		{verificationDelivery: "feishu", verificationWebhook: "http://example.test/hook", verificationAllowedEmails: map[string]struct{}{"allowed@example.com": {}}},
		{verificationDelivery: "unknown"},
	} {
		if s.VerificationCodeDeliveryAvailable() {
			t.Fatal("unsafe delivery configuration allowed")
		}
	}
}
