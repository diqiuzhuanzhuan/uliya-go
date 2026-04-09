package main

import (
	"encoding/json"
	"fmt"
	"iter"
	"regexp"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/loong/uliya-go/tools/movetool"
)

const (
	stateKeyTargetPath           = "target_path"
	stateKeyOrganizationIntent   = "organization_intent"
	stateKeyOrganizePending      = "organize_pending"
	stateKeyPendingField         = "organize_pending_field"
	stateKeyAwaitingConfirmation = "awaiting_confirmation"
)

var pathPattern = regexp.MustCompile(`(?i)(~?[/\\][^\s"'，。；;]+|[A-Za-z]:\\[^\s"'，。；;]+|/(Users|tmp|var|etc|home|opt|private|Volumes)/[^\s"'，。；;]+)`)

// confirmationTool manages the plan-confirmation state machine.
// action="request"  → plan presented, waiting for user
// action="confirm"  → user approved, clear flag and reset op log
// action="cancel"   → user declined, clear all task state
type confirmationTool struct{}

func (t *confirmationTool) Name() string { return "request_confirmation" }

func (t *confirmationTool) Description() string {
	return `Manage the confirmation gate before executing file operations.
Call with action="request" immediately after presenting your plan to the user — do not execute any changes until this is confirmed.
Call with action="confirm" when the user approves execution.
Call with action="cancel" when the user cancels the operation.`
}

func (t *confirmationTool) IsLongRunning() bool { return false }

func (t *confirmationTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		ParametersJsonSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"request", "confirm", "cancel"},
					"description": `"request": mark plan as pending user approval. "confirm": user approved, ready to execute. "cancel": user cancelled.`,
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (t *confirmationTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if req == nil {
		return nil
	}
	if req.Tools == nil {
		req.Tools = make(map[string]any)
	}
	if _, exists := req.Tools[t.Name()]; exists {
		return fmt.Errorf("duplicate tool: %s", t.Name())
	}
	req.Tools[t.Name()] = t
	if req.Config == nil {
		req.Config = &genai.GenerateContentConfig{}
	}
	for _, candidate := range req.Config.Tools {
		if candidate != nil && candidate.FunctionDeclarations != nil {
			candidate.FunctionDeclarations = append(candidate.FunctionDeclarations, t.Declaration())
			return nil
		}
	}
	req.Config.Tools = append(req.Config.Tools, &genai.Tool{
		FunctionDeclarations: []*genai.FunctionDeclaration{t.Declaration()},
	})
	return nil
}

func (t *confirmationTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("tool context is required")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}
	var input struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("decode args: %w", err)
	}
	switch input.Action {
	case "request":
		if err := ctx.State().Set(stateKeyAwaitingConfirmation, "true"); err != nil {
			return nil, err
		}
	case "confirm":
		if err := ctx.State().Set(stateKeyAwaitingConfirmation, ""); err != nil {
			return nil, err
		}
		_ = movetool.ClearLog()
	case "cancel":
		_ = ctx.State().Set(stateKeyAwaitingConfirmation, "")
		_ = ctx.State().Set(stateKeyTargetPath, "")
		_ = ctx.State().Set(stateKeyOrganizationIntent, "")
		_ = ctx.State().Set(stateKeyOrganizePending, "false")
	default:
		return nil, fmt.Errorf("unknown action: %s", input.Action)
	}
	return map[string]any{"action": input.Action}, nil
}

type intakeAnalysis struct {
	Relevant            bool   `json:"relevant"`
	Path                string `json:"path"`
	Intent              string `json:"intent"`
	UseCurrentWorkspace bool   `json:"use_current_workspace"`
}

func newRootAgent(model model.LLM, repoRoot string, fileTools []tool.Tool, bashTool tool.Tool, todoTools []tool.Tool, moveTools []tool.Tool) (agent.Agent, error) {
	organizerAgent, err := llmagent.New(llmagent.Config{
		Name:        "organizer_agent",
		Model:       model,
		Description: "Executes concrete file-organization tasks after the target path and organization intent are clear.",
		Instruction: `You are the execution and planning stage of Uliya's file-organization workflow.
You run only after the task is concrete enough to execute.
Target path:
{target_path?}
Organization intent:
{organization_intent?}
Awaiting confirmation:
{awaiting_confirmation?}
Current todo list (empty if none):
{todo_list?}
{temp:todo_refresh_reminder?}

PLANNING AND CONFIRMATION — FOLLOW THIS SEQUENCE STRICTLY:

PHASE 1 — PLAN (awaiting_confirmation is empty or "false"):
- Scan the target directory fully with list_files or find_files.
- Generate an explicit plan: for every file, state the operation (e.g., "Move photo.jpg → Images/photo.jpg"). Group by category with totals.
- Present the plan clearly to the user and ask for confirmation.
- Call request_confirmation(action="request") immediately after. DO NOT execute any file changes in this phase.

PHASE 2 — EXECUTE (awaiting_confirmation is "true"):
- Read the user's latest message.
- If CONFIRMED (yes / ok / go ahead / 好 / 确认 / 执行 / 是 etc.):
  1. Call request_confirmation(action="confirm") first.
  2. Execute the plan step by step using the todo list.
  3. Use move_file for all file moves and renames. Use create_dir for new directories.
  4. Use bash only for operations that move_file and create_dir cannot handle.
- If CANCELLED (no / cancel / stop / 取消 / 不要 / 算了 etc.):
  1. Call request_confirmation(action="cancel").
  2. Tell the user the operation was cancelled.
- If the user MODIFIES the plan: update plan, present again, call request_confirmation(action="request").

OTHER RULES:
- If the latest user message is only a greeting or casual conversation, reply briefly in the user's language, do not use tools, and do not create a todo list.
- If target_path or organization_intent is missing, do not create a todo list and do not use filesystem tools. Ask for the missing information concisely instead.
- Keep folder names, category names, explanations, and final output aligned with the user's language unless the user explicitly asks otherwise.
- Never claim that you cannot access the local filesystem unless a real tool call has failed.
- Every time you call write_todo, send the full list. Mark items in_progress before doing them and completed immediately after finishing them.
- After every real tool action, check whether the current in_progress todo has been completed and update it before continuing.`,
		Tools:                append(append(append(todoTools, fileTools...), moveTools...), bashTool, &confirmationTool{}),
		BeforeToolCallbacks:  []llmagent.BeforeToolCallback{requireConcreteTaskBeforeTool},
		AfterToolCallbacks:   []llmagent.AfterToolCallback{syncTodoAfterTool},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{todoReminderBeforeModel},
	})
	if err != nil {
		return nil, err
	}

	intakeAgent, err := newIntakeAgent(model)
	if err != nil {
		return nil, err
	}

	_ = repoRoot
	return agent.New(agent.Config{
		Name:        "uliya_workflow_agent",
		Description: "A workflow-based file organization agent with an intake stage and an execution stage.",
		SubAgents:   []agent.Agent{intakeAgent, organizerAgent},
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				// When awaiting confirmation, skip intake and go directly to organizer.
				if strings.EqualFold(getStateString(ctx.Session().State(), stateKeyAwaitingConfirmation), "true") {
					for event, err := range organizerAgent.Run(ctx) {
						if !yield(event, err) {
							return
						}
					}
					return
				}

				intakeHandledThisTurn := false
				for event, err := range intakeAgent.Run(ctx) {
					if err != nil || (event != nil && event.Content != nil && len(event.Content.Parts) > 0) {
						intakeHandledThisTurn = true
					}
					if !yield(event, err) {
						return
					}
					if err != nil {
						return
					}
				}

				if intakeHandledThisTurn || strings.EqualFold(getStateString(ctx.Session().State(), stateKeyOrganizePending), "true") {
					return
				}

				for event, err := range organizerAgent.Run(ctx) {
					if !yield(event, err) {
						return
					}
				}
			}
		},
	})
}

func newIntakeAgent(intakeModel model.LLM) (agent.Agent, error) {
	return agent.New(agent.Config{
		Name:        "intake_agent",
		Description: "Collects the target path and organization intent before file-organization execution begins.",
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				userText := strings.TrimSpace(contentPlainText(ctx.UserContent()))
				if userText == "" {
					return
				}

				existingPath := getStateString(ctx.Session().State(), stateKeyTargetPath)
				existingIntent := getStateString(ctx.Session().State(), stateKeyOrganizationIntent)
				pending := strings.EqualFold(getStateString(ctx.Session().State(), stateKeyOrganizePending), "true")
				pendingField := getStateString(ctx.Session().State(), stateKeyPendingField)
				analysis := analyzeIntakeMessage(ctx, intakeModel, userText, existingPath, existingIntent, pendingField)
				if !analysis.Relevant && !pending {
					return
				}

				path := strings.TrimSpace(analysis.Path)
				if path == "" && analysis.UseCurrentWorkspace {
					path = "."
				}
				if path == "" {
					path = existingPath
				}

				intent := strings.TrimSpace(analysis.Intent)
				if intent == "" {
					intent = existingIntent
				}

				if path != "" {
					_ = ctx.Session().State().Set(stateKeyTargetPath, path)
				}
				if intent != "" {
					_ = ctx.Session().State().Set(stateKeyOrganizationIntent, intent)
				}

				if path == "" {
					_ = ctx.Session().State().Set(stateKeyOrganizePending, "true")
					_ = ctx.Session().State().Set(stateKeyPendingField, "path")
					ctx.EndInvocation()
					yield(stateTextEvent(ctx.InvocationID(), generateIntakeQuestion(ctx, intakeModel, userText, path, intent, "path"), map[string]any{
						stateKeyOrganizePending:    "true",
						stateKeyPendingField:       "path",
						stateKeyOrganizationIntent: intent,
					}), nil)
					return
				}

				if intent == "" {
					_ = ctx.Session().State().Set(stateKeyOrganizePending, "true")
					_ = ctx.Session().State().Set(stateKeyPendingField, "intent")
					ctx.EndInvocation()
					yield(stateTextEvent(ctx.InvocationID(), generateIntakeQuestion(ctx, intakeModel, userText, path, intent, "intent"), map[string]any{
						stateKeyTargetPath:      path,
						stateKeyOrganizePending: "true",
						stateKeyPendingField:    "intent",
					}), nil)
					return
				}

				_ = ctx.Session().State().Set(stateKeyOrganizePending, "false")
				yield(stateOnlyEvent(ctx.InvocationID(), map[string]any{
					stateKeyTargetPath:         path,
					stateKeyOrganizationIntent: intent,
					stateKeyOrganizePending:    "false",
					stateKeyPendingField:       "",
				}), nil)
			}
		},
	})
}

func stateTextEvent(invocationID, text string, delta map[string]any) *session.Event {
	event := session.NewEvent(invocationID)
	event.Content = genai.NewContentFromText(text, genai.RoleModel)
	event.Actions.StateDelta = delta
	return event
}

func stateOnlyEvent(invocationID string, delta map[string]any) *session.Event {
	event := session.NewEvent(invocationID)
	event.Actions.StateDelta = delta
	return event
}

func getStateString(state session.State, key string) string {
	if state == nil {
		return ""
	}
	value, err := state.Get(key)
	if err != nil || value == nil {
		return ""
	}
	s, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func extractTargetPath(text string) string {
	matches := pathPattern.FindStringSubmatch(text)
	if len(matches) > 0 {
		return strings.TrimSpace(matches[0])
	}
	return ""
}

func analyzeIntakeMessage(ctx agent.InvocationContext, intakeModel model.LLM, userText, existingPath, existingIntent, pendingField string) intakeAnalysis {
	if intakeModel == nil {
		return fallbackIntakeAnalysis(userText, pendingField)
	}

	systemPrompt := `You analyze the latest user message for a file-organization assistant.
Return exactly one JSON object with this schema:
{
  "relevant": boolean,
  "path": string,
  "intent": string,
  "use_current_workspace": boolean
}

Guidelines:
- Set "relevant" to true if the message starts, continues, or answers a file-organization task.
- If the assistant is already waiting for a missing field, treat a natural user reply as part of that same task.
- Fill "path" only when the user explicitly provides a directory path.
- Set "use_current_workspace" to true only when the user clearly refers to the current directory or repo.
- Fill "intent" only when the user explicitly states how they want the files organized, or when they are directly answering a prior question asking for the rule.
- If the message is unrelated small talk and there is no ongoing intake, set "relevant" to false.
- Do not invent missing information.
- Output JSON only.`

	knownPath := existingPath
	if strings.TrimSpace(knownPath) == "" {
		knownPath = "(unknown)"
	}
	knownIntent := existingIntent
	if strings.TrimSpace(knownIntent) == "" {
		knownIntent = "(unknown)"
	}
	if strings.TrimSpace(pendingField) == "" {
		pendingField = "(none)"
	}

	userPrompt := fmt.Sprintf("Latest user message:\n%s\n\nKnown path: %s\nKnown organization intent: %s\nPending field: %s", userText, knownPath, knownIntent, pendingField)
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText(systemPrompt, "system"),
			genai.NewContentFromText(userPrompt, genai.RoleUser),
		},
	}

	for resp, err := range intakeModel.GenerateContent(ctx, req, false) {
		if err != nil {
			break
		}
		if analysis, ok := parseIntakeAnalysis(contentPlainText(resp.Content)); ok {
			return analysis
		}
	}

	return fallbackIntakeAnalysis(userText, pendingField)
}

func parseIntakeAnalysis(text string) (intakeAnalysis, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return intakeAnalysis{}, false
	}

	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var analysis intakeAnalysis
	if err := json.Unmarshal([]byte(text), &analysis); err != nil {
		return intakeAnalysis{}, false
	}
	analysis.Path = strings.TrimSpace(analysis.Path)
	analysis.Intent = strings.TrimSpace(analysis.Intent)
	return analysis, true
}

func fallbackIntakeAnalysis(text, pendingField string) intakeAnalysis {
	text = strings.TrimSpace(text)
	if text == "" {
		return intakeAnalysis{}
	}

	if pendingField == "intent" {
		return intakeAnalysis{
			Relevant: true,
			Intent:   text,
		}
	}

	path := extractTargetPath(text)
	if path == "" {
		return intakeAnalysis{}
	}

	return intakeAnalysis{
		Relevant: true,
		Path:     path,
	}
}

func generateIntakeQuestion(ctx agent.InvocationContext, intakeModel model.LLM, userText, path, intent, missingField string) string {
	if intakeModel == nil {
		return intakeQuestionFallback(missingField)
	}

	systemPrompt := `You are the intake stage of a file-organization assistant.
Your job is to ask exactly one short follow-up question for the single missing piece of information.
Rules:
- Reply in the same language as the user's latest message.
- Ask only one question.
- Be concise and natural.
- Do not mention tools, permissions, workflows, or internal logic.
- Do not ask for information that is already known.
- If the missing field is "path", ask only for the directory path.
- If the missing field is "intent", ask only how the user wants the files organized.`

	knownPath := path
	if strings.TrimSpace(knownPath) == "" {
		knownPath = "(unknown)"
	}
	knownIntent := intent
	if strings.TrimSpace(knownIntent) == "" {
		knownIntent = "(unknown)"
	}

	userPrompt := fmt.Sprintf("Latest user message:\n%s\n\nKnown path: %s\nKnown organization intent: %s\nMissing field: %s", userText, knownPath, knownIntent, missingField)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText(systemPrompt, "system"),
			genai.NewContentFromText(userPrompt, genai.RoleUser),
		},
	}

	for resp, err := range intakeModel.GenerateContent(ctx, req, false) {
		if err != nil {
			break
		}
		text := strings.TrimSpace(contentPlainText(resp.Content))
		if text != "" {
			return text
		}
	}

	return intakeQuestionFallback(missingField)
}

func intakeQuestionFallback(missingField string) string {
	if missingField == "intent" {
		return "How would you like the files organized? / 请说下整理规则？"
	}
	return "Which directory? / 请提供目录路径？"
}

func requireConcreteTaskBeforeTool(ctx tool.Context, calledTool tool.Tool, args map[string]any) (map[string]any, error) {
	if ctx == nil || calledTool == nil {
		return nil, nil
	}
	if !hasConcreteTaskDefinition(ctx.State()) {
		return nil, fmt.Errorf("ask the user for both the target path and the organization rule before creating a todo list or using filesystem tools")
	}
	return nil, nil
}

func hasConcreteTaskDefinition(state session.State) bool {
	return hasConcreteTaskValues(
		getStateString(state, stateKeyTargetPath),
		getStateString(state, stateKeyOrganizationIntent),
	)
}

func hasConcreteTaskValues(path, intent string) bool {
	return strings.TrimSpace(path) != "" && strings.TrimSpace(intent) != ""
}
