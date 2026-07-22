package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeAutomationQueries is an in-memory AutomationQueries for notifier tests.
type fakeAutomationQueries struct {
	installation Installation
	installErr   error
	autopilot    db.Autopilot
	run          db.AutopilotRun
	runErr       error
	issue        db.Issue
	issueErr     error
	task         db.AgentTaskQueue
	taskErr      error
}

func (f *fakeAutomationQueries) GetLarkInstallationByAppID(ctx context.Context, appID string) (Installation, error) {
	if f.installErr != nil {
		return Installation{}, f.installErr
	}
	return f.installation, nil
}

func (f *fakeAutomationQueries) GetAutopilotRun(ctx context.Context, id pgtype.UUID) (db.AutopilotRun, error) {
	if f.runErr != nil {
		return db.AutopilotRun{}, f.runErr
	}
	return f.run, nil
}

func (f *fakeAutomationQueries) GetAutopilot(ctx context.Context, id pgtype.UUID) (db.Autopilot, error) {
	return f.autopilot, nil
}

func (f *fakeAutomationQueries) GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error) {
	if f.issueErr != nil {
		return db.Issue{}, f.issueErr
	}
	return f.issue, nil
}

func (f *fakeAutomationQueries) GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error) {
	if f.taskErr != nil {
		return db.AgentTaskQueue{}, f.taskErr
	}
	return f.task, nil
}

func newAutomationNotifier(t *testing.T, client APIClient, q AutomationQueries) *AutomationNotifier {
	t.Helper()
	return NewAutomationNotifier(AutomationNotifierConfig{
		APIClient:   client,
		Credentials: fakeCredentials{secret: "shh"},
		Queries:     q,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// uuidBytesString formats 16 bytes as the canonical UUID string the notifier parses
// back via pgtype.UUID.Scan. Avoids depending on pgtype's Valuer impl.
func uuidBytesString(b [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestAutomationNotifier_CompletedWithIssue_SendsGreenCard(t *testing.T) {
	apID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	issueID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	q := &fakeAutomationQueries{
		installation: Installation{AppID: automationAppID, Status: string(InstallationActive)},
		autopilot:    db.Autopilot{Title: "每日站会总结"},
		run:          db.AutopilotRun{AutopilotID: apID, IssueID: issueID, Status: "completed"},
		issue:        db.Issue{Number: 42, Title: "整理站会纪要", Status: "done"},
	}
	client := &stubAPIClientWithRecorder{configured: true}
	n := newAutomationNotifier(t, client, q)

	n.handle(events.Event{
		Type: protocol.EventAutopilotRunDone,
		Payload: map[string]any{
			"run_id": uuidBytesString(runID.Bytes),
			"status": "completed",
		},
	})

	if len(client.interactiveOut) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.interactiveOut))
	}
	card := client.interactiveOut[0]
	if card.ChatID != automationChatID {
		t.Errorf("chat_id = %q, want %q", card.ChatID, automationChatID)
	}
	if !strings.Contains(card.CardJSON, "\"template\":\"green\"") {
		t.Errorf("expected green header, got %s", card.CardJSON)
	}
	if !strings.Contains(card.CardJSON, "每日站会总结") {
		t.Errorf("missing autopilot title: %s", card.CardJSON)
	}
	if !strings.Contains(card.CardJSON, "#42") || !strings.Contains(card.CardJSON, "整理站会纪要") {
		t.Errorf("missing issue ref: %s", card.CardJSON)
	}
}

func TestAutomationNotifier_Failed_IncludesFailureReason(t *testing.T) {
	apID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	q := &fakeAutomationQueries{
		installation: Installation{AppID: automationAppID},
		autopilot:    db.Autopilot{Title: "构建镜像"},
		run: db.AutopilotRun{
			AutopilotID:   apID,
			Status:        "failed",
			FailureReason: pgtype.Text{String: "docker daemon offline", Valid: true},
		},
	}
	client := &stubAPIClientWithRecorder{configured: true}
	n := newAutomationNotifier(t, client, q)

	n.handle(events.Event{
		Type: protocol.EventAutopilotRunDone,
		Payload: map[string]any{
			"run_id": uuidBytesString(runID.Bytes),
			"status": "failed",
		},
	})

	if len(client.interactiveOut) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.interactiveOut))
	}
	card := client.interactiveOut[0]
	if !strings.Contains(card.CardJSON, "\"template\":\"red\"") {
		t.Errorf("expected red header, got %s", card.CardJSON)
	}
	if !strings.Contains(card.CardJSON, "docker daemon offline") {
		t.Errorf("missing failure reason: %s", card.CardJSON)
	}
}

func TestAutomationNotifier_RunOnly_IncludesOutput(t *testing.T) {
	apID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	taskID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	result := mustJSON(t, protocol.TaskCompletedPayload{TaskID: "t", Output: "所有检查通过 ✓"})
	q := &fakeAutomationQueries{
		installation: Installation{AppID: automationAppID},
		autopilot:    db.Autopilot{Title: "健康检查"},
		run:          db.AutopilotRun{AutopilotID: apID, TaskID: taskID, Status: "completed", Result: result},
		task:         db.AgentTaskQueue{ID: taskID, Result: result},
	}
	client := &stubAPIClientWithRecorder{configured: true}
	n := newAutomationNotifier(t, client, q)

	n.handle(events.Event{
		Type: protocol.EventAutopilotRunDone,
		Payload: map[string]any{
			"run_id": uuidBytesString(runID.Bytes),
			"status": "completed",
		},
	})

	if len(client.interactiveOut) != 1 {
		t.Fatalf("expected 1 send, got %d", len(client.interactiveOut))
	}
	if !strings.Contains(client.interactiveOut[0].CardJSON, "所有检查通过") {
		t.Errorf("missing task output: %s", client.interactiveOut[0].CardJSON)
	}
}

func TestAutomationNotifier_Skipped_DoesNotSend(t *testing.T) {
	q := &fakeAutomationQueries{installation: Installation{AppID: automationAppID}}
	client := &stubAPIClientWithRecorder{configured: true}
	n := newAutomationNotifier(t, client, q)

	n.handle(events.Event{
		Type:    protocol.EventAutopilotRunDone,
		Payload: map[string]any{"run_id": uuidBytesString([16]byte{2}), "status": "skipped"},
	})

	if len(client.interactiveOut) != 0 {
		t.Fatalf("expected no send for skipped, got %d", len(client.interactiveOut))
	}
}

func TestAutomationNotifier_UnknownApp_Silent(t *testing.T) {
	q := &fakeAutomationQueries{
		installErr: pgx.ErrNoRows,
		run:        db.AutopilotRun{AutopilotID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		autopilot:  db.Autopilot{Title: "x"},
	}
	client := &stubAPIClientWithRecorder{configured: true}
	n := newAutomationNotifier(t, client, q)

	n.handle(events.Event{
		Type:    protocol.EventAutopilotRunDone,
		Payload: map[string]any{"run_id": uuidBytesString([16]byte{2}), "status": "completed"},
	})

	if len(client.interactiveOut) != 0 {
		t.Fatalf("expected no send when app not installed, got %d", len(client.interactiveOut))
	}
}

func TestAutomationNotifier_StubClient_DoesNotSend(t *testing.T) {
	q := &fakeAutomationQueries{installation: Installation{AppID: automationAppID}}
	client := &stubAPIClientWithRecorder{configured: false} // simulates NewStubAPIClient
	n := newAutomationNotifier(t, client, q)

	n.handle(events.Event{
		Type:    protocol.EventAutopilotRunDone,
		Payload: map[string]any{"run_id": uuidBytesString([16]byte{2}), "status": "completed"},
	})

	if len(client.interactiveOut) != 0 {
		t.Fatalf("expected no send when client not configured, got %d", len(client.interactiveOut))
	}
}

func TestAutomationNotifier_Register_SubscribesRunDone(t *testing.T) {
	bus := events.New()
	n := newAutomationNotifier(t, &stubAPIClientWithRecorder{configured: true}, &fakeAutomationQueries{
		installation: Installation{AppID: automationAppID},
		run:          db.AutopilotRun{AutopilotID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		autopilot:    db.Autopilot{Title: "x"},
	})
	// Should not panic / should wire cleanly.
	n.Register(bus)
	// nil bus must be a safe no-op.
	n.Register(nil)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestDecodeAutomationOutput_RuneSafeTruncation(t *testing.T) {
	// Multibyte output: ensure truncation never splits a rune and stays
	// valid UTF-8 (Chinese "任务" chars are 3 bytes each).
	body := strings.Repeat("任务", 1000) // 6000 bytes, 2000 runes
	result := mustJSON(t, protocol.TaskCompletedPayload{Output: body})

	got := decodeAutomationOutput(result)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected truncation ellipsis, suffix=%q", got[len(got)-3:])
	}
	// Capped at automationOutputCap runes + the ellipsis.
	if rc := utf8.RuneCountInString(got); rc != automationOutputCap+1 {
		t.Errorf("rune count = %d, want %d", rc, automationOutputCap+1)
	}
}

func TestDecodeAutomationOutput_EmptyAndMalformed(t *testing.T) {
	if got := decodeAutomationOutput(nil); got != "" {
		t.Errorf("nil result = %q, want empty", got)
	}
	if got := decodeAutomationOutput([]byte("not json")); got != "" {
		t.Errorf("malformed result = %q, want empty", got)
	}
	got := decodeAutomationOutput(mustJSON(t, protocol.TaskCompletedPayload{Output: "  hi  "}))
	if got != "hi" {
		t.Errorf("expected trimmed output %q, got %q", "hi", got)
	}
}
