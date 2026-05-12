package main

import (
	"errors"
	"fmt"
	"iter"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/loong/uliya-go/openaimodel"
)

const (
	stateKeyTargetPath           = "target_path"
	stateKeyOrganizationIntent   = "organization_intent"
	stateKeyOrganizePending      = "organize_pending"
	stateKeyPendingField         = "organize_pending_field"
	stateKeyAwaitingConfirmation = "awaiting_confirmation"
	stateKeyResponseLanguage     = "response_language"
)

var pathPattern = regexp.MustCompile(`(?i)(~?[/\\][^\s"'，。；;]+|[A-Za-z]:\\[^\s"'，。；;]+|/(Users|tmp|var|etc|home|opt|private|Volumes)/[^\s"'，。；;]+)`)
var hanPattern = regexp.MustCompile(`\p{Han}`)
var latinLetterPattern = regexp.MustCompile(`[A-Za-z]`)

type intakeAnalysis struct {
	Relevant            bool   `json:"relevant"`
	Path                string `json:"path"`
	Intent              string `json:"intent"`
	UseCurrentWorkspace bool   `json:"use_current_workspace"`
}

func newRootAgent(model model.LLM, repoRoot string, fileTools []tool.Tool, bashTool tool.Tool, todoTools []tool.Tool, moveTools []tool.Tool) (agent.Agent, error) {
	intakeAgent, err := newIntakeAgent(model)
	if err != nil {
		return nil, err
	}

	organizerAgent, err := newOrganizerAgent(model, repoRoot, fileTools, bashTool, todoTools)
	if err != nil {
		return nil, err
	}

	executorAgent, err := newExecutorAgent(repoRoot, moveTools, bashTool)
	if err != nil {
		return nil, err
	}

	return agent.New(agent.Config{
		Name:        "uliya_workflow_agent",
		Description: "A workflow-based file organization agent with intake, planning, and execution stages.",
		SubAgents:   []agent.Agent{intakeAgent, organizerAgent, executorAgent},
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				userText := strings.TrimSpace(contentPlainText(ctx.UserContent()))
				updateResponseLanguageFromInput(ctx.Session().State(), userText)
				if strings.EqualFold(getStateString(ctx.Session().State(), stateKeyAwaitingConfirmation), "true") {
					switch {
					case isExecutionConfirmed(userText):
						for event, err := range executorAgent.Run(ctx) {
							if !yield(event, err) {
								return
							}
							if err != nil {
								return
							}
						}
						return
					case isExecutionCancelled(userText):
						if todoResult, err := clearTodoState(ctx.Session().State()); err == nil {
							if !yield(todoResultEvent(ctx.InvocationID(), todoResult), nil) {
								return
							}
						}
						yield(stateTextEvent(ctx.InvocationID(), localizeText(ctx.Session().State(), "已取消本次整理。", "Operation cancelled."), withResponseLanguage(ctx.Session().State(), clearedWorkflowStateDelta())), nil)
						return
					case userText != "":
						yield(stateOnlyEvent(ctx.InvocationID(), withResponseLanguage(ctx.Session().State(), map[string]any{
							stateKeyAwaitingConfirmation: "",
							stateKeyOrganizationIntent:   userText,
						})), nil)
					default:
						return
					}
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

				if !hasConcreteTaskDefinition(ctx.Session().State()) {
					return
				}

				targetPath := getStateString(ctx.Session().State(), stateKeyTargetPath)
				if _, _, err := resolveOrganizationPath(repoRoot, targetPath); err != nil {
					yield(stateTextEvent(ctx.InvocationID(), formatTargetPathValidationError(targetPath, err, prefersChinese(ctx.Session().State())), withResponseLanguage(ctx.Session().State(), map[string]any{
						stateKeyAwaitingConfirmation: "",
						stateKeyOrganizePending:      "true",
						stateKeyPendingField:         "path",
					})), nil)
					return
				}

				var (
					plan   organizationPlan
					review planReview
					err    error
				)

				yield(stateOnlyEvent(ctx.InvocationID(), map[string]any{
					stateKeyPlanningToolCalls:    0,
					stateKeyPlanningObservations: "",
					stateKeyExecutionPlan:        "",
					stateKeyExecutionReview:      "",
				}), nil)

				for event, err := range organizerAgent.Run(ctx) {
					if !yield(event, err) {
						return
					}
					if err != nil {
						return
					}
				}

				plan, err = loadOrganizationPlanFromState(ctx.Session().State())
				if err != nil {
					yield(nil, err)
					return
				}
				review = mergeReviewWithValidation(planReview{Approved: true}, validateCommandPlan(plan))
				if len(plan.Commands) == 0 {
					inventory, err := collectPlanningInventory(repoRoot, ctx.Session().State())
					if err != nil {
						yield(nil, err)
						return
					}
					if len(inventory.Files) == 0 {
						if todoResult, err := clearTodoState(ctx.Session().State()); err == nil {
							if !yield(todoResultEvent(ctx.InvocationID(), todoResult), nil) {
								return
							}
						}
						yield(stateTextEvent(ctx.InvocationID(), localizeText(ctx.Session().State(), "目标目录里没有可整理的文件。", "No files found in the target directory."), withResponseLanguage(ctx.Session().State(), clearedWorkflowStateDelta())), nil)
						return
					}
					review = mergeReviewWithValidation(review, validateOrganizationPlan(plan, inventory))
				}

				if !review.Approved {
					if todoResult, err := clearTodoState(ctx.Session().State()); err == nil {
						if !yield(todoResultEvent(ctx.InvocationID(), todoResult), nil) {
							return
						}
					}
					yield(stateTextEvent(ctx.InvocationID(), formatPlanIssues(review, prefersChinese(ctx.Session().State())), withResponseLanguage(ctx.Session().State(), map[string]any{
						stateKeyAwaitingConfirmation: "",
					})), nil)
					return
				}

				if len(plan.Moves) == 0 && len(plan.Commands) == 0 {
					if todoResult, err := clearTodoState(ctx.Session().State()); err == nil {
						if !yield(todoResultEvent(ctx.InvocationID(), todoResult), nil) {
							return
						}
					}
					yield(stateTextEvent(ctx.InvocationID(), localizeText(ctx.Session().State(), "审核后的计划不需要移动任何文件。", "The reviewed plan does not require any file moves."), withResponseLanguage(ctx.Session().State(), clearedWorkflowStateDelta())), nil)
					return
				}

				todoResult, err := initializeFullPhaseTodos(ctx.Session().State(), plan)
				if err != nil {
					yield(nil, err)
					return
				}
				if !yield(todoResultEvent(ctx.InvocationID(), todoResult), nil) {
					return
				}

				yield(stateTextEvent(ctx.InvocationID(), formatPlanForConfirmation(plan, review, prefersChinese(ctx.Session().State())), withResponseLanguage(ctx.Session().State(), map[string]any{
					stateKeyAwaitingConfirmation: "true",
					stateKeyOrganizePending:      "false",
					stateKeyPendingField:         "",
				})), nil)
				return
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
					if reply := generateCasualReply(ctx, intakeModel, userText); reply != "" {
						ctx.EndInvocation()
						yield(stateTextEvent(ctx.InvocationID(), reply, withResponseLanguage(ctx.Session().State(), nil)), nil)
					}
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
					yield(stateTextEvent(ctx.InvocationID(), generateIntakeQuestion(ctx, intakeModel, userText, path, intent, "path"), withResponseLanguage(ctx.Session().State(), map[string]any{
						stateKeyOrganizePending:    "true",
						stateKeyPendingField:       "path",
						stateKeyOrganizationIntent: intent,
					})), nil)
					return
				}

				if intent == "" {
					_ = ctx.Session().State().Set(stateKeyOrganizePending, "true")
					_ = ctx.Session().State().Set(stateKeyPendingField, "intent")
					ctx.EndInvocation()
					yield(stateTextEvent(ctx.InvocationID(), generateIntakeQuestion(ctx, intakeModel, userText, path, intent, "intent"), withResponseLanguage(ctx.Session().State(), map[string]any{
						stateKeyTargetPath:      path,
						stateKeyOrganizePending: "true",
						stateKeyPendingField:    "intent",
					})), nil)
					return
				}

				_ = ctx.Session().State().Set(stateKeyOrganizePending, "false")
				yield(stateOnlyEvent(ctx.InvocationID(), withResponseLanguage(ctx.Session().State(), map[string]any{
					stateKeyTargetPath:         path,
					stateKeyOrganizationIntent: intent,
					stateKeyOrganizePending:    "false",
					stateKeyPendingField:       "",
				})), nil)
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

func withResponseLanguage(state session.State, delta map[string]any) map[string]any {
	lang := getStateString(state, stateKeyResponseLanguage)
	if lang == "" {
		return delta
	}
	if delta == nil {
		delta = map[string]any{}
	}
	delta[stateKeyResponseLanguage] = lang
	return delta
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

func updateResponseLanguageFromInput(state session.State, text string) {
	if state == nil {
		return
	}
	lang := detectResponseLanguage(getStateString(state, stateKeyResponseLanguage), text)
	if lang == "" {
		return
	}
	_ = state.Set(stateKeyResponseLanguage, lang)
}

func detectResponseLanguage(current, text string) string {
	text = strings.TrimSpace(text)
	current = strings.TrimSpace(current)
	if text == "" {
		if current != "" {
			return current
		}
		return "en"
	}
	if hanPattern.MatchString(text) {
		return "zh"
	}
	if current != "" && isStandalonePathReply(text) {
		return current
	}
	if current != "" && utf8.RuneCountInString(text) <= 12 {
		return current
	}
	if latinLetterPattern.MatchString(text) {
		return "en"
	}
	if current != "" {
		return current
	}
	return "en"
}

func isStandalonePathReply(text string) bool {
	path := extractTargetPath(text)
	return path != "" && strings.TrimSpace(path) == strings.TrimSpace(text)
}

func prefersChinese(state session.State) bool {
	return getStateString(state, stateKeyResponseLanguage) == "zh"
}

func localizeText(state session.State, zh, en string) string {
	if prefersChinese(state) {
		return zh
	}
	return en
}

func formatTargetPathValidationError(path string, err error, preferChinese bool) string {
	path = strings.TrimSpace(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if preferChinese {
			return fmt.Sprintf("目标目录不存在：%s\n请确认目录路径后再发我一次。", path)
		}
		return fmt.Sprintf("The target directory does not exist: %s\nPlease confirm the directory path and send it again.", path)
	case strings.Contains(strings.ToLower(err.Error()), "not a directory"):
		if preferChinese {
			return fmt.Sprintf("目标路径不是目录：%s\n请提供一个可访问的目录路径。", path)
		}
		return fmt.Sprintf("The target path is not a directory: %s\nPlease provide an accessible directory path.", path)
	default:
		if preferChinese {
			return fmt.Sprintf("无法访问目标目录：%s\n错误信息：%v\n请确认路径是否正确，或检查挂载和权限后再试。", path, err)
		}
		return fmt.Sprintf("Unable to access the target directory: %s\nError: %v\nPlease confirm the path or check mounts and permissions, then try again.", path, err)
	}
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

	for resp, err := range intakeModel.GenerateContent(openaimodel.WithLogLabel(ctx, "intake-analysis"), req, false) {
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
	analysis, err := parseJSONBlock[intakeAnalysis](text)
	if err != nil {
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
		return intakeQuestionFallback(missingField, prefersChinese(ctx.Session().State()))
	}

	preferredLanguage := strings.TrimSpace(getStateString(ctx.Session().State(), stateKeyResponseLanguage))
	if preferredLanguage == "" {
		preferredLanguage = detectResponseLanguage("", userText)
		if preferredLanguage == "" {
			preferredLanguage = "en"
		}
	}

	systemPrompt := `You are the intake stage of a file-organization assistant.
Your job is to ask exactly one short follow-up question for the single missing piece of information.
Rules:
- Reply in the session's preferred reply language.
- Ask only one question.
- Be concise and natural.
- Do not mention tools, permissions, workflows, or internal logic.
- Do not ask for information that is already known.
- If the missing field is "path", ask only for the directory path.
- If the missing field is "intent", ask only how the user wants the files organized.
- The preferred reply language will be provided as "zh" or "en". If it is "zh", reply in Simplified Chinese. If it is "en", reply in English.`

	knownPath := path
	if strings.TrimSpace(knownPath) == "" {
		knownPath = "(unknown)"
	}
	knownIntent := intent
	if strings.TrimSpace(knownIntent) == "" {
		knownIntent = "(unknown)"
	}

	userPrompt := fmt.Sprintf("Latest user message:\n%s\n\nKnown path: %s\nKnown organization intent: %s\nMissing field: %s\nPreferred reply language: %s", userText, knownPath, knownIntent, missingField, preferredLanguage)

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText(systemPrompt, "system"),
			genai.NewContentFromText(userPrompt, genai.RoleUser),
		},
	}

	for resp, err := range intakeModel.GenerateContent(openaimodel.WithLogLabel(ctx, "intake-question"), req, false) {
		if err != nil {
			break
		}
		text := strings.TrimSpace(contentPlainText(resp.Content))
		if text != "" {
			return text
		}
	}

	return intakeQuestionFallback(missingField, prefersChinese(ctx.Session().State()))
}

func intakeQuestionFallback(missingField string, preferChinese bool) string {
	if missingField == "intent" {
		if preferChinese {
			return "你希望我按什么规则整理这些文件？"
		}
		return "How would you like the files organized?"
	}
	if preferChinese {
		return "请提供要整理的目录路径。"
	}
	return "Which directory should I organize?"
}

func hasConcreteTaskValues(path, intent string) bool {
	return strings.TrimSpace(path) != "" && strings.TrimSpace(intent) != ""
}

func hasConcreteTaskDefinition(state session.State) bool {
	if state == nil {
		return false
	}
	return hasConcreteTaskValues(
		getStateString(state, stateKeyTargetPath),
		getStateString(state, stateKeyOrganizationIntent),
	)
}

func generateCasualReply(ctx agent.InvocationContext, intakeModel model.LLM, userText string) string {
	if intakeModel == nil {
		return ""
	}

	systemPrompt := `You are the front-desk reply layer of a file-organization assistant.
The latest user message is not a file-organization request.
Reply briefly in the same language as the user.
Rules:
- Keep it to one short sentence.
- Be friendly and direct.
- Invite the user to provide a directory path and an organization rule.
- Do not mention tools, prompts, internal logic, or workflows.`

	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText(systemPrompt, "system"),
			genai.NewContentFromText(userText, genai.RoleUser),
		},
	}

	for resp, err := range intakeModel.GenerateContent(openaimodel.WithLogLabel(ctx, "casual-reply"), req, false) {
		if err != nil {
			break
		}
		text := strings.TrimSpace(contentPlainText(resp.Content))
		if text != "" {
			return text
		}
	}
	return ""
}
