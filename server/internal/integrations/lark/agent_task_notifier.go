package lark

// AgentTaskNotifier pushes completed issue-task results to the same fixed
// Feishu operations chat used by AutomationNotifier. It deliberately ignores
// chat and autopilot tasks: those already have their own outbound paths.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// agentTaskOutputChunkRunes keeps every card comfortably below Lark's message
// size limit while preserving the complete result across multiple cards.
const agentTaskOutputChunkRunes = 3000

// AgentTaskNotificationQueries is the data-layer seam needed to turn a
// task:completed event into a human-readable Feishu notification.
type AgentTaskNotificationQueries interface {
	GetLarkInstallationByAppID(ctx context.Context, appID string) (Installation, error)
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
	ListPullRequestsByIssue(ctx context.Context, issueID pgtype.UUID) ([]db.ListPullRequestsByIssueRow, error)
}

type AgentTaskNotifierConfig struct {
	APIClient   APIClient
	Credentials CredentialsResolver
	Queries     AgentTaskNotificationQueries
	AppURL      string
	Logger      *slog.Logger
}

// AgentTaskNotifier is best-effort: notification failures are logged and do
// not turn a successfully completed agent task into an HTTP failure.
type AgentTaskNotifier struct {
	client      APIClient
	credentials CredentialsResolver
	queries     AgentTaskNotificationQueries
	appURL      string
	log         *slog.Logger
}

func NewAgentTaskNotifier(cfg AgentTaskNotifierConfig) *AgentTaskNotifier {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &AgentTaskNotifier{
		client:      cfg.APIClient,
		credentials: cfg.Credentials,
		queries:     cfg.Queries,
		appURL:      strings.TrimRight(strings.TrimSpace(cfg.AppURL), "/"),
		log:         log,
	}
}

func (n *AgentTaskNotifier) Register(bus *events.Bus) {
	if bus == nil {
		return
	}
	bus.Subscribe(protocol.EventTaskCompleted, n.handle)
}

func (n *AgentTaskNotifier) handle(e events.Event) {
	if n.client == nil || !n.client.IsConfigured() || n.credentials == nil || n.queries == nil {
		return
	}
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	taskID, _ := payload["task_id"].(string)
	if taskID == "" {
		return
	}
	taskUUID, err := util.ParseUUID(taskID)
	if err != nil {
		n.log.Warn("lark agent task notifier: parse task_id", "task_id", taskID, "error", err)
		return
	}

	ctx := context.Background()
	task, err := n.queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		n.log.Warn("lark agent task notifier: load task", "task_id", taskID, "error", err)
		return
	}
	// Only ordinary issue work belongs in the operations notification. Direct
	// chat has its own reply path, and autopilot emits autopilot:run_done.
	if !task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || task.Status != "completed" {
		return
	}

	issue, err := n.queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		n.log.Warn("lark agent task notifier: load issue", "task_id", taskID, "error", err)
		return
	}

	inst, err := n.queries.GetLarkInstallationByAppID(ctx, automationAppID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			n.log.Warn("lark agent task notifier: resolve installation", "app_id", automationAppID, "error", err)
		}
		return
	}
	creds, err := buildInstallationCredentials(n.credentials, inst)
	if err != nil {
		n.log.Warn("lark agent task notifier: build credentials", "app_id", automationAppID, "error", err)
		return
	}

	cards, err := n.buildCards(ctx, task, issue)
	if err != nil {
		n.log.Warn("lark agent task notifier: build cards", "task_id", taskID, "error", err)
		return
	}
	for i, card := range cards {
		if _, err := n.client.SendInteractiveCard(ctx, SendCardParams{
			InstallationID: creds,
			ChatID:         automationChatID,
			CardJSON:       card,
		}); err != nil {
			n.log.Warn("lark agent task notifier: send card",
				"chat_id", automationChatID,
				"task_id", taskID,
				"part", i+1,
				"parts", len(cards),
				"error", err,
			)
		}
	}
}

func (n *AgentTaskNotifier) buildCards(ctx context.Context, task db.AgentTaskQueue, issue db.Issue) ([]string, error) {
	agentName := "智能体"
	if agent, err := n.queries.GetAgent(ctx, task.AgentID); err == nil && strings.TrimSpace(agent.Name) != "" {
		agentName = agent.Name
	}

	identifier := fmt.Sprintf("#%d", issue.Number)
	workspaceSlug := ""
	if workspace, err := n.queries.GetWorkspace(ctx, issue.WorkspaceID); err == nil {
		workspaceSlug = workspace.Slug
		if workspace.IssuePrefix != "" {
			identifier = fmt.Sprintf("%s-%d", workspace.IssuePrefix, issue.Number)
		}
	}

	output, resultPRURL := decodeAgentTaskResult(task.Result)
	prURLs := make([]string, 0, 2)
	prURLs = appendUniqueURL(prURLs, resultPRURL)
	if linked, err := n.queries.ListPullRequestsByIssue(ctx, issue.ID); err == nil {
		for _, pr := range linked {
			prURLs = appendUniqueURL(prURLs, pr.HtmlUrl)
		}
	} else {
		n.log.Warn("lark agent task notifier: list pull requests",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
	}

	chunks := splitAgentTaskOutput(output, agentTaskOutputChunkRunes)
	if len(chunks) == 0 {
		chunks = []string{"（智能体未返回文本处理结果）"}
	}

	cards := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		lines := make([]string, 0, 6+len(prURLs))
		if i == 0 {
			lines = append(lines, fmt.Sprintf("**智能体：** %s", escapeLarkMarkdown(agentName)))
			issueLabel := identifier + " " + issue.Title
			if issueURL := n.issueURL(workspaceSlug, identifier); issueURL != "" {
				lines = append(lines, fmt.Sprintf("**issue：** [%s](%s)", escapeLarkMarkdown(issueLabel), issueURL))
			} else {
				lines = append(lines, fmt.Sprintf("**issue：** %s", escapeLarkMarkdown(issueLabel)))
			}
			for _, prURL := range prURLs {
				if safeURL := safeMarkdownURL(prURL); safeURL != "" {
					lines = append(lines, fmt.Sprintf("**PR：** [打开 PR](%s)", safeURL))
				} else if strings.TrimSpace(prURL) != "" {
					lines = append(lines, fmt.Sprintf("**PR：** %s", escapeLarkMarkdown(prURL)))
				}
			}
		}
		if len(chunks) == 1 {
			lines = append(lines, "**完整处理结果：**")
		} else {
			lines = append(lines, fmt.Sprintf("**完整处理结果（%d/%d）：**", i+1, len(chunks)))
		}
		lines = append(lines, chunk)

		header := "智能体 task 完成"
		if len(chunks) > 1 {
			header = fmt.Sprintf("智能体 task 完成（%d/%d）", i+1, len(chunks))
		}
		card, err := renderAgentTaskResultCard(header, strings.Join(lines, "\n"))
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func decodeAgentTaskResult(result []byte) (output, prURL string) {
	if len(result) == 0 {
		return "", ""
	}
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return "", ""
	}
	return strings.TrimSpace(util.UnescapeBackslashEscapes(payload.Output)), strings.TrimSpace(payload.PRURL)
}

func splitAgentTaskOutput(output string, maxRunes int) []string {
	if output == "" || maxRunes <= 0 {
		return nil
	}
	runes := []rune(output)
	parts := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > maxRunes {
		cut := maxRunes
		// Prefer a nearby line boundary without producing very small chunks.
		for i := maxRunes; i >= maxRunes*3/4; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

func appendUniqueURL(urls []string, candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return urls
	}
	for _, existing := range urls {
		if existing == candidate {
			return urls
		}
	}
	return append(urls, candidate)
}

func (n *AgentTaskNotifier) issueURL(workspaceSlug, identifier string) string {
	if n.appURL == "" || workspaceSlug == "" || identifier == "" {
		return ""
	}
	return n.appURL + "/" + url.PathEscape(workspaceSlug) + "/issues/" + url.PathEscape(identifier)
}

func safeMarkdownURL(raw string) string {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return strings.ReplaceAll(parsed.String(), ")", "%29")
}

func escapeLarkMarkdown(raw string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]")
	return replacer.Replace(raw)
}

func renderAgentTaskResultCard(header, body string) (string, error) {
	doc := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "green",
			"title":    map[string]any{"tag": "plain_text", "content": header},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": body,
				},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
