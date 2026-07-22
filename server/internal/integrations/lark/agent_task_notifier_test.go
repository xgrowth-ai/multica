package lark

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeAgentTaskNotificationQueries struct {
	installation Installation
	task         db.AgentTaskQueue
	issue        db.Issue
	agent        db.Agent
	workspace    db.Workspace
	pullRequests []db.ListPullRequestsByIssueRow
}

func (f *fakeAgentTaskNotificationQueries) GetLarkInstallationByAppID(context.Context, string) (Installation, error) {
	return f.installation, nil
}

func (f *fakeAgentTaskNotificationQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return f.task, nil
}

func (f *fakeAgentTaskNotificationQueries) GetIssue(context.Context, pgtype.UUID) (db.Issue, error) {
	return f.issue, nil
}

func (f *fakeAgentTaskNotificationQueries) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return f.agent, nil
}

func (f *fakeAgentTaskNotificationQueries) GetWorkspace(context.Context, pgtype.UUID) (db.Workspace, error) {
	return f.workspace, nil
}

func (f *fakeAgentTaskNotificationQueries) ListPullRequestsByIssue(context.Context, pgtype.UUID) ([]db.ListPullRequestsByIssueRow, error) {
	return f.pullRequests, nil
}

func newAgentTaskNotifier(client APIClient, queries AgentTaskNotificationQueries) *AgentTaskNotifier {
	return NewAgentTaskNotifier(AgentTaskNotifierConfig{
		APIClient:   client,
		Credentials: fakeCredentials{secret: "secret"},
		Queries:     queries,
		AppURL:      "https://multica.example/",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func agentTaskNotificationFixture(t *testing.T, output, prURL string) *fakeAgentTaskNotificationQueries {
	t.Helper()
	taskID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	issueID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	agentID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	workspaceID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	result, err := json.Marshal(protocol.TaskCompletedPayload{
		TaskID: uuidString(taskID),
		PRURL:  prURL,
		Output: output,
	})
	if err != nil {
		t.Fatalf("marshal task result: %v", err)
	}
	return &fakeAgentTaskNotificationQueries{
		installation: Installation{AppID: automationAppID, Status: string(InstallationActive)},
		task: db.AgentTaskQueue{
			ID:      taskID,
			IssueID: issueID,
			AgentID: agentID,
			Status:  "completed",
			Result:  result,
		},
		issue: db.Issue{
			ID:          issueID,
			WorkspaceID: workspaceID,
			Number:      42,
			Title:       "实现任务通知",
		},
		agent:     db.Agent{ID: agentID, Name: "Codex"},
		workspace: db.Workspace{ID: workspaceID, Slug: "研发 团队", IssuePrefix: "MUL"},
		pullRequests: []db.ListPullRequestsByIssueRow{
			{HtmlUrl: "https://github.com/xgrowth-ai/multica/pull/99"},
		},
	}
}

func TestAgentTaskNotifier_CompletedIssueTaskSendsFullResultAndPRLinks(t *testing.T) {
	output := strings.Repeat("完整处理结果✓", 700)
	callbackPR := "https://github.com/xgrowth-ai/multica/pull/100"
	queries := agentTaskNotificationFixture(t, output, callbackPR)
	client := &stubAPIClientWithRecorder{configured: true}
	notifier := newAgentTaskNotifier(client, queries)

	notifier.handle(events.Event{
		Type: protocol.EventTaskCompleted,
		Payload: map[string]any{
			"task_id": uuidString(queries.task.ID),
			"status":  "completed",
		},
	})

	parts := splitAgentTaskOutput(output, agentTaskOutputChunkRunes)
	if len(parts) < 2 {
		t.Fatalf("test output should span multiple cards, got %d", len(parts))
	}
	if len(client.interactiveOut) != len(parts) {
		t.Fatalf("sent cards = %d, want %d", len(client.interactiveOut), len(parts))
	}
	if got := strings.Join(parts, ""); got != output {
		t.Fatal("split output did not preserve the complete result")
	}

	first := client.interactiveOut[0]
	if first.ChatID != automationChatID {
		t.Fatalf("chat id = %q, want %q", first.ChatID, automationChatID)
	}
	for _, want := range []string{
		"Codex",
		"MUL-42",
		"实现任务通知",
		"https://multica.example/",
		callbackPR,
		"https://github.com/xgrowth-ai/multica/pull/99",
		parts[0][:30],
	} {
		if !strings.Contains(first.CardJSON, want) {
			t.Errorf("first card missing %q: %s", want, first.CardJSON)
		}
	}
	last := client.interactiveOut[len(client.interactiveOut)-1]
	if !strings.Contains(last.CardJSON, parts[len(parts)-1]) {
		t.Error("last card does not contain the final result chunk")
	}
}

func TestAgentTaskNotifier_RegisterSubscribesTaskCompleted(t *testing.T) {
	queries := agentTaskNotificationFixture(t, "done", "")
	client := &stubAPIClientWithRecorder{configured: true}
	notifier := newAgentTaskNotifier(client, queries)
	bus := events.New()
	notifier.Register(bus)
	notifier.Register(nil)

	bus.Publish(events.Event{
		Type:    protocol.EventTaskCompleted,
		Payload: map[string]any{"task_id": uuidString(queries.task.ID)},
	})

	if len(client.interactiveOut) != 1 {
		t.Fatalf("sent cards = %d, want 1", len(client.interactiveOut))
	}
}

func TestAgentTaskNotifier_AttemptsEveryPartAfterSendFailure(t *testing.T) {
	queries := agentTaskNotificationFixture(t, strings.Repeat("结果", agentTaskOutputChunkRunes+1), "")
	client := &stubAPIClientWithRecorder{configured: true, sendErr: context.DeadlineExceeded}

	newAgentTaskNotifier(client, queries).handle(events.Event{
		Type:    protocol.EventTaskCompleted,
		Payload: map[string]any{"task_id": uuidString(queries.task.ID)},
	})

	want := len(splitAgentTaskOutput(strings.Repeat("结果", agentTaskOutputChunkRunes+1), agentTaskOutputChunkRunes))
	if len(client.interactiveOut) != want {
		t.Fatalf("send attempts = %d, want %d", len(client.interactiveOut), want)
	}
}

func TestAgentTaskNotifier_FiltersTasksWithExistingOutboundPath(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*db.AgentTaskQueue)
	}{
		{
			name: "autopilot",
			mutate: func(task *db.AgentTaskQueue) {
				task.AutopilotRunID = pgtype.UUID{Bytes: [16]byte{8}, Valid: true}
			},
		},
		{
			name: "chat",
			mutate: func(task *db.AgentTaskQueue) {
				task.ChatSessionID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
			},
		},
		{
			name: "no issue",
			mutate: func(task *db.AgentTaskQueue) {
				task.IssueID = pgtype.UUID{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := agentTaskNotificationFixture(t, "done", "")
			tt.mutate(&queries.task)
			client := &stubAPIClientWithRecorder{configured: true}
			newAgentTaskNotifier(client, queries).handle(events.Event{
				Type:    protocol.EventTaskCompleted,
				Payload: map[string]any{"task_id": uuidString(queries.task.ID)},
			})
			if len(client.interactiveOut) != 0 {
				t.Fatalf("sent cards = %d, want 0", len(client.interactiveOut))
			}
		})
	}
}

func TestDecodeAgentTaskResult_UnescapesLineBreaksAndKeepsPR(t *testing.T) {
	result, err := json.Marshal(protocol.TaskCompletedPayload{
		Output: `line one\nline two`,
		PRURL:  " https://github.com/xgrowth-ai/multica/pull/101 ",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	output, prURL := decodeAgentTaskResult(result)
	if output != "line one\nline two" {
		t.Fatalf("output = %q", output)
	}
	if prURL != "https://github.com/xgrowth-ai/multica/pull/101" {
		t.Fatalf("pr url = %q", prURL)
	}
}

func TestSafeMarkdownURL_RejectsNonHTTP(t *testing.T) {
	if got := safeMarkdownURL("javascript:alert(1)"); got != "" {
		t.Fatalf("unsafe URL accepted: %q", got)
	}
	if got := safeMarkdownURL("https://github.com/org/repo/pull/1"); got == "" {
		t.Fatal("valid HTTPS URL rejected")
	}
}
