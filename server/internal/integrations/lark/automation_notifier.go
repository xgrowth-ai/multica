package lark

// AutomationNotifier pushes automation (autopilot) run results to a fixed
// Feishu chat through the existing Lark Open Platform App API — the same
// transport the Patcher uses for chat replies. It is the outbound counterpart
// for autopilot runs, which have no originating chat_session and therefore
// never reach the Patcher (see outbound.go processEvent early-return).
//
// Scope is intentionally narrow: a single hardcoded target chat and sender
// app, subscribed to autopilot:run_done. The bot for automationAppID must
// already be a member of automationChatID (the repo has no add-to-chat API).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// automationChatID is the Feishu chat that receives automation run cards.
// Hardcoded per deployment choice; retarget by editing this constant.
const automationChatID = ChatID("oc_76d42114468f43029041def47fb40c2e")

// automationAppID is the Lark app_id (cli_…) whose installation authenticates
// the outbound send. The matching bot must be a member of automationChatID.
const automationAppID = "cli_aad2d9a53eb85cb6"

// automationOutputCap bounds the agent output excerpt rendered into the card
// so a runaway run can't blow past Lark's message size limit.
const automationOutputCap = 1500

// AutomationQueries is the data-layer seam the notifier needs. *ChannelStore
// satisfies it because it embeds *db.Queries.
type AutomationQueries interface {
	GetLarkInstallationByAppID(ctx context.Context, appID string) (Installation, error)
	GetAutopilotRun(ctx context.Context, id pgtype.UUID) (db.AutopilotRun, error)
	GetAutopilot(ctx context.Context, id pgtype.UUID) (db.Autopilot, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
}

// AutomationNotifierConfig wires the notifier. Mirrors OutcomeReplierConfig.
type AutomationNotifierConfig struct {
	APIClient   APIClient
	Credentials CredentialsResolver
	Queries     AutomationQueries
	Logger      *slog.Logger
}

// AutomationNotifier subscribes to autopilot:run_done and posts a result card
// to automationChatID via the Lark App API. Best-effort: errors are logged,
// never returned to the bus.
type AutomationNotifier struct {
	client      APIClient
	credentials CredentialsResolver
	queries     AutomationQueries
	log         *slog.Logger
}

// NewAutomationNotifier constructs the notifier. A nil logger falls back to
// slog.Default(). The notifier is inert until Register is called.
func NewAutomationNotifier(cfg AutomationNotifierConfig) *AutomationNotifier {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &AutomationNotifier{
		client:      cfg.APIClient,
		credentials: cfg.Credentials,
		queries:     cfg.Queries,
		log:         log,
	}
}

// Register subscribes the notifier to autopilot:run_done on the bus.
func (n *AutomationNotifier) Register(bus *events.Bus) {
	if bus == nil {
		return
	}
	bus.Subscribe(protocol.EventAutopilotRunDone, n.handle)
}

// handle renders and sends one automation run card. Skipped runs are silent
// (dedup/no-op); only completed and failed runs notify.
func (n *AutomationNotifier) handle(e events.Event) {
	if n.client == nil || !n.client.IsConfigured() {
		return
	}
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	runID, _ := payload["run_id"].(string)
	if runID == "" {
		return
	}
	status, _ := payload["status"].(string)
	if status == "skipped" {
		return
	}

	ctx := context.Background()
	runIDUUID, err := util.ParseUUID(runID)
	if err != nil {
		n.log.Warn("lark automation notifier: parse run_id", "run_id", runID, "error", err)
		return
	}

	run, err := n.queries.GetAutopilotRun(ctx, runIDUUID)
	if err != nil {
		n.log.Warn("lark automation notifier: load autopilot run", "run_id", runID, "error", err)
		return
	}

	// Resolve the sender installation before any card rendering so an
	// unconfigured deployment skips the GetAutopilot/GetIssue lookups.
	inst, err := n.queries.GetLarkInstallationByAppID(ctx, automationAppID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Configured app isn't installed in this deployment — notifier
			// stays inert. Not an error worth noise.
			return
		}
		n.log.Warn("lark automation notifier: resolve installation", "app_id", automationAppID, "error", err)
		return
	}
	creds, err := buildInstallationCredentials(n.credentials, inst)
	if err != nil {
		n.log.Warn("lark automation notifier: build credentials", "app_id", automationAppID, "error", err)
		return
	}

	card, err := n.buildCard(ctx, run, status)
	if err != nil {
		n.log.Warn("lark automation notifier: build card", "run_id", runID, "error", err)
		return
	}

	if _, err := n.client.SendInteractiveCard(ctx, SendCardParams{
		InstallationID: creds,
		ChatID:         automationChatID,
		CardJSON:       card,
	}); err != nil {
		n.log.Warn("lark automation notifier: send card", "chat_id", automationChatID, "run_id", runID, "error", err)
	}
}

// buildCard assembles the interactive-card JSON for one run. The header color
// signals outcome; the body carries the autopilot title, the linked issue (if
// any) or the run_only task output, and the failure reason.
func (n *AutomationNotifier) buildCard(ctx context.Context, run db.AutopilotRun, status string) (string, error) {
	apTitle := "自动化任务"
	if ap, err := n.queries.GetAutopilot(ctx, run.AutopilotID); err == nil && strings.TrimSpace(ap.Title) != "" {
		apTitle = ap.Title
	}

	headerText, template := "自动化任务完成", "green"
	if status == "failed" {
		headerText, template = "自动化任务失败", "red"
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("**%s**", apTitle))

	if run.IssueID.Valid {
		if issue, err := n.queries.GetIssue(ctx, run.IssueID); err == nil {
			lines = append(lines, fmt.Sprintf("关联事项：#%d %s（%s）", issue.Number, issue.Title, issue.Status))
		}
	} else if run.TaskID.Valid {
		if task, err := n.queries.GetAgentTask(ctx, run.TaskID); err == nil {
			if out := decodeAutomationOutput(task.Result); out != "" {
				lines = append(lines, fmt.Sprintf("运行结果：\n%s", out))
			}
		}
	}

	if run.FailureReason.Valid && run.FailureReason.String != "" {
		lines = append(lines, fmt.Sprintf("失败原因：%s", run.FailureReason.String))
	}

	doc := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": template,
			"title":    map[string]any{"tag": "plain_text", "content": headerText},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": strings.Join(lines, "\n"),
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

// decodeAutomationOutput extracts the agent output from a run/task Result
// blob (protocol.TaskCompletedPayload), trimmed and capped for card display.
// Truncation is rune-aware so Chinese/multibyte output never splits mid-rune.
func decodeAutomationOutput(result []byte) string {
	if len(result) == 0 {
		return ""
	}
	var p protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &p); err != nil {
		return ""
	}
	out := strings.TrimSpace(p.Output)
	r := []rune(out)
	if len(r) <= automationOutputCap {
		return out
	}
	return string(r[:automationOutputCap]) + "…"
}
