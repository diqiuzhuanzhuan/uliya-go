package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"iter"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/loong/uliya-go/tools/movetool"
	"github.com/loong/uliya-go/tools/todotool"
)

const (
	stateKeyExecutionPlan          = "execution_plan_json"
	stateKeyExecutionReview        = "execution_plan_review_json"
	stateKeyPlanningToolCalls      = "planning_tool_calls"
	stateKeyPlanningObservations   = "planning_observations_json"
	maxPlanningInspectionToolCalls = 6
)

type organizationInventory struct {
	Root  string          `json:"root"`
	Files []inventoryFile `json:"files"`
}

type inventoryFile struct {
	Path         string `json:"path"`
	Ext          string `json:"ext,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	ModifiedTime string `json:"modified_time,omitempty"`
}

type organizationPlan struct {
	Summary     string             `json:"summary"`
	Directories []string           `json:"directories,omitempty"`
	Moves       []organizationMove `json:"moves"`
	Notes       []string           `json:"notes,omitempty"`
	Commands    []string           `json:"commands,omitempty"`
}

type organizationMove struct {
	Src    string `json:"src"`
	Dst    string `json:"dst"`
	Reason string `json:"reason,omitempty"`
}

type planReview struct {
	Approved bool     `json:"approved"`
	Issues   []string `json:"issues,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type planningObservationRecord struct {
	Tool   string `json:"tool"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
}

type executionResult struct {
	Moved      []organizationMove
	CreatedDir []string
	Failures   []string
	TodoList   string
	Commands   []string
}

type executionStep struct {
	Label      string
	ActiveForm string
	Run        func() error
}

func newOrganizerAgent(planningModel model.LLM, repoRoot string, fileTools []tool.Tool, bashTool tool.Tool, todoTools []tool.Tool) (agent.Agent, error) {
	_ = repoRoot

	tools := concatTools(nonNilTools(bashTool), fileTools, todoTools)
	return llmagent.New(llmagent.Config{
		Name:            "organization_agent",
		Model:           planningModel,
		Description:     "Inspects the target directory with tools, tracks progress with todos, and returns a conservative organization plan.",
		IncludeContents: llmagent.IncludeContentsNone,
		OutputKey:       stateKeyExecutionPlan,
		Tools:           tools,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			todoReminderBeforeModel,
			appendPlanningBudgetReminderBeforeModel,
		},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			validatePlanningToolCall,
		},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			recordPlanningObservationAfterTool,
			syncTodoAfterTool,
		},
		Instruction: `You are a file-organization planning agent.
Return JSON only with this schema:
{"summary":"string","directories":["string"],"moves":[{"src":"string","dst":"string","reason":"string"}],"commands":["string"],"notes":["string"]}

Rules:
- First make the task concrete: identify the target directory and the organization intent.
- For multi-step work, use write_todo to keep a short current plan and progress state.
- Inspect the directory with tools before deciding on the final plan.
- Prefer bash for cheap metadata inspection such as find, ls, stat, du, sort, uniq, awk, sed, and cut.
- Prefer file tools over bash when you need structured file lookups or exact file reads.
- Do not read file contents unless the user's intent clearly requires semantic/content-based grouping.
- Do not mutate the filesystem while planning. The planning phase is read-only.
- Keep inspection tight. After a small number of tool calls, stop and return the final JSON plan instead of continuing to explore.
- Shell commands in the final plan must be macOS/BSD compatible and assume they run from the target directory.
- Use "commands" for batch-safe operations and "moves" for explicit per-file moves.
- Keep the plan conservative, reversible, and aligned with the user's stated intent.
- Before returning, self-check for safety: no destructive commands, no writes outside the target directory, and no accidental renames unless the user asked for them.

Target path: {target_path?}
Organization intent: {organization_intent?}`,
	})
}

func newExecutorAgent(repoRoot string, moveTools []tool.Tool, bashTool tool.Tool) (agent.Agent, error) {
	return agent.New(agent.Config{
		Name:        "executor_agent",
		Description: "Executes the reviewed organization plan deterministically using local filesystem tools.",
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				plan, err := loadOrganizationPlanFromState(ctx.Session().State())
				if err != nil {
					yield(nil, err)
					return
				}

				targetPath := getStateString(ctx.Session().State(), stateKeyTargetPath)
				absRoot, _, err := resolveOrganizationPath(repoRoot, targetPath)
				if err != nil {
					yield(nil, err)
					return
				}

				result, todoEvents, err := executeOrganizationPlan(ctx.InvocationID(), ctx.Session().State(), absRoot, plan, moveTools, bashTool)
				for _, event := range todoEvents {
					if !yield(event, nil) {
						return
					}
				}
				if err != nil {
					yield(stateTextEvent(ctx.InvocationID(), fmt.Sprintf("Execution stopped: %v", err), map[string]any{
						stateKeyAwaitingConfirmation: "",
					}), nil)
					return
				}

				yield(stateTextEvent(ctx.InvocationID(), formatExecutionResult(result), clearedWorkflowStateDelta()), nil)
			}
		},
	})
}

func collectPlanningInventory(repoRoot string, state session.State) (organizationInventory, error) {
	targetPath := getStateString(state, stateKeyTargetPath)
	absRoot, _, err := resolveOrganizationPath(repoRoot, targetPath)
	if err != nil {
		return organizationInventory{}, err
	}

	inventory, err := collectOrganizationInventory(absRoot)
	if err != nil {
		return organizationInventory{}, err
	}

	return inventory, nil
}

func nonNilTools(candidates ...tool.Tool) []tool.Tool {
	tools := make([]tool.Tool, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil {
			tools = append(tools, candidate)
		}
	}
	return tools
}

func concatTools(groups ...[]tool.Tool) []tool.Tool {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	tools := make([]tool.Tool, 0, total)
	for _, group := range groups {
		tools = append(tools, group...)
	}
	return tools
}

func resolveOrganizationPath(repoRoot, requested string) (string, string, error) {
	requested = strings.TrimSpace(requested)
	logToolCall("uliya_workflow_agent", "resolve_target_path", map[string]any{
		"repo_root": repoRoot,
		"requested": requested,
	})
	if requested == "" {
		err := fmt.Errorf("target path is required")
		logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{"ok": false, "error": err.Error()})
		return "", "", err
	}

	candidate := requested
	if strings.HasPrefix(candidate, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			if candidate == "~" {
				candidate = home
			} else if strings.HasPrefix(candidate, "~/") {
				candidate = filepath.Join(home, candidate[2:])
			}
		}
	} else if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(repoRoot, candidate)
	}

	absPath, err := filepath.Abs(candidate)
	if err != nil {
		err = fmt.Errorf("resolve target path: %w", err)
		logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{"ok": false, "error": err.Error()})
		return "", "", err
	}

	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// Keep the original absolute path if it is not a symlink or symlink evaluation is unavailable.
		resolvedPath = absPath
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		err = fmt.Errorf("stat target path: %w", err)
		logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{"ok": false, "absolute_path": absPath, "resolved_path": resolvedPath, "error": err.Error()})
		return "", "", err
	}
	if !info.IsDir() {
		err := fmt.Errorf("target path is not a directory: %s", requested)
		logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{"ok": false, "absolute_path": absPath, "resolved_path": resolvedPath, "error": err.Error()})
		return "", "", err
	}

	rel, err := filepath.Rel(repoRoot, resolvedPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{
			"ok":            true,
			"absolute_path": absPath,
			"resolved_path": resolvedPath,
			"display_path":  requested,
		})
		return resolvedPath, requested, nil
	}
	if rel == "." {
		logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{
			"ok":            true,
			"absolute_path": absPath,
			"resolved_path": resolvedPath,
			"display_path":  ".",
		})
		return resolvedPath, ".", nil
	}
	displayPath := filepath.ToSlash(rel)
	logToolResponse("uliya_workflow_agent", "resolve_target_path", map[string]any{
		"ok":            true,
		"absolute_path": absPath,
		"resolved_path": resolvedPath,
		"display_path":  displayPath,
	})
	return resolvedPath, displayPath, nil
}

func collectOrganizationInventory(root string) (organizationInventory, error) {
	inventory := organizationInventory{Root: root}
	logToolCall("uliya_workflow_agent", "scan_inventory", map[string]any{"root": root})
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		inventory.Files = append(inventory.Files, inventoryFile{
			Path:         filepath.ToSlash(rel),
			Ext:          strings.ToLower(filepath.Ext(d.Name())),
			SizeBytes:    info.Size(),
			ModifiedTime: info.ModTime().UTC().Format(time.RFC3339),
		})
		return nil
	})
	if err != nil {
		err = fmt.Errorf("scan inventory: %w", err)
		logToolResponse("uliya_workflow_agent", "scan_inventory", map[string]any{"ok": false, "root": root, "error": err.Error()})
		return organizationInventory{}, err
	}

	sort.Slice(inventory.Files, func(i, j int) bool {
		return inventory.Files[i].Path < inventory.Files[j].Path
	})
	logToolResponse("uliya_workflow_agent", "scan_inventory", map[string]any{
		"ok":           true,
		"root":         root,
		"file_count":   len(inventory.Files),
		"sample_files": sampleInventoryPaths(inventory.Files, 10),
	})
	return inventory, nil
}

func sampleInventoryPaths(files []inventoryFile, max int) []string {
	if len(files) == 0 || max <= 0 {
		return nil
	}
	if len(files) < max {
		max = len(files)
	}
	sample := make([]string, 0, max)
	for i := 0; i < max; i++ {
		sample = append(sample, files[i].Path)
	}
	return sample
}

func loadOrganizationPlanFromState(state session.State) (organizationPlan, error) {
	plan, err := loadJSONState[organizationPlan](state, stateKeyExecutionPlan)
	if err != nil {
		return organizationPlan{}, err
	}
	return normalizePlan(plan), nil
}

func loadJSONState[T any](state session.State, key string) (T, error) {
	var zero T
	if state == nil {
		return zero, fmt.Errorf("session state is not available")
	}
	raw := getStateString(state, key)
	if raw == "" {
		return zero, fmt.Errorf("state key %q is empty", key)
	}
	return parseJSONBlock[T](raw)
}

func parseJSONBlock[T any](raw string) (T, error) {
	var zero T
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return zero, fmt.Errorf("decode json block: %w", err)
	}
	return out, nil
}

func normalizePlan(plan organizationPlan) organizationPlan {
	plan.Summary = strings.TrimSpace(plan.Summary)

	directories := make([]string, 0, len(plan.Directories))
	seenDir := make(map[string]bool)
	for _, dir := range plan.Directories {
		dir = cleanRelativePlanPath(dir)
		if dir == "" || dir == "." || seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		directories = append(directories, dir)
	}
	sort.Strings(directories)
	plan.Directories = directories

	notes := make([]string, 0, len(plan.Notes))
	for _, note := range plan.Notes {
		note = strings.TrimSpace(note)
		if note != "" {
			notes = append(notes, note)
		}
	}
	plan.Notes = notes

	commands := make([]string, 0, len(plan.Commands))
	seenCommand := make(map[string]bool)
	for _, command := range plan.Commands {
		command = strings.TrimSpace(command)
		if command == "" || seenCommand[command] {
			continue
		}
		seenCommand[command] = true
		commands = append(commands, command)
	}
	plan.Commands = commands

	moves := make([]organizationMove, 0, len(plan.Moves))
	seenMove := make(map[string]bool)
	for _, move := range plan.Moves {
		move.Src = cleanRelativePlanPath(move.Src)
		move.Dst = cleanRelativePlanPath(move.Dst)
		move.Reason = strings.TrimSpace(move.Reason)
		if move.Src == "" || move.Dst == "" || move.Src == move.Dst {
			continue
		}
		key := move.Src + "->" + move.Dst
		if seenMove[key] {
			continue
		}
		seenMove[key] = true
		moves = append(moves, move)
	}
	sort.Slice(moves, func(i, j int) bool {
		if moves[i].Dst == moves[j].Dst {
			return moves[i].Src < moves[j].Src
		}
		return moves[i].Dst < moves[j].Dst
	})
	plan.Moves = moves
	return plan
}

type bashCommandResult struct {
	Command  string `json:"command"`
	Workdir  string `json:"workdir"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func runBashToolCommand(root string, bashTool tool.Tool, command string) (bashCommandResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return bashCommandResult{}, fmt.Errorf("bash command is required")
	}

	if runner, ok := bashTool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	}); ok && bashTool != nil {
		result, err := runner.Run(nil, map[string]any{
			"command": command,
			"workdir": root,
		})
		if err != nil {
			return bashCommandResult{}, err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return bashCommandResult{}, err
		}
		var decoded bashCommandResult
		if err := json.Unmarshal(data, &decoded); err != nil {
			return bashCommandResult{}, err
		}
		if decoded.ExitCode != 0 {
			return decoded, fmt.Errorf("bash command failed with exit code %d: %s", decoded.ExitCode, strings.TrimSpace(decoded.Stderr))
		}
		return decoded, nil
	}

	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return bashCommandResult{
			Command: command,
			Workdir: root,
			Stdout:  string(output),
			Stderr:  string(output),
		}, err
	}
	return bashCommandResult{
		Command: command,
		Workdir: root,
		Stdout:  string(output),
	}, nil
}

func validatePlanningToolCall(ctx tool.Context, calledTool tool.Tool, args map[string]any) (map[string]any, error) {
	if ctx == nil || calledTool == nil || ctx.AgentName() != "organization_agent" {
		return nil, nil
	}
	toolName := calledTool.Name()
	switch toolName {
	case "write_todo", "read_todo":
		return nil, nil
	case "edit_file", "write_file", "move_file", "create_dir":
		return nil, fmt.Errorf("%s is not allowed during planning; the planning phase is read-only", toolName)
	}
	if !isPlanningInspectionTool(toolName) {
		return nil, nil
	}
	if current := getStateInt(ctx.State(), stateKeyPlanningToolCalls); current >= maxPlanningInspectionToolCalls {
		return nil, fmt.Errorf("planning inspection budget exhausted; return the final JSON plan using the observations already collected")
	}
	if toolName == "bash" {
		command := strings.TrimSpace(stringArg(args, "command"))
		if command == "" {
			return nil, fmt.Errorf("planning bash command is required")
		}
		if err := validateDiscoveryCommand(command); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func validateDiscoveryCommand(command string) error {
	normalized := strings.ToLower(strings.TrimSpace(command))
	forbiddenFragments := []string{
		" -printf",
		"mv ",
		" mv ",
		"\nmv ",
		"rm ",
		" rm ",
		"\nrm ",
		"chmod ",
		"chown ",
		"touch ",
		"mkdir ",
		" cp ",
		"\ncp ",
		"python ",
		"python3 ",
		"node ",
		"perl ",
		"ruby ",
		"cat ",
		"head -c ",
		"tail -c ",
	}
	for _, fragment := range forbiddenFragments {
		if strings.Contains(normalized, fragment) {
			return fmt.Errorf("discovery command is not allowed in shell discovery: %s", strings.TrimSpace(command))
		}
	}
	return nil
}

func buildPlanningObservationSummary(state session.State) string {
	records := loadPlanningObservationRecords(state)
	observations := make([]string, 0, len(records))
	for i, record := range records {
		line := fmt.Sprintf("%d. [%s]", i+1, record.Tool)
		if record.Input != "" {
			line += " input=" + trimForObservation(record.Input, 160)
		}
		if record.Output != "" {
			line += " output=" + trimForObservation(record.Output, 220)
		}
		observations = append(observations, line)
	}
	if len(observations) == 0 {
		return "No planning observations were recorded."
	}
	return strings.Join(observations, "\n")
}

func loadPlanningObservationRecords(state session.State) []planningObservationRecord {
	raw := getStateString(state, stateKeyPlanningObservations)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var records []planningObservationRecord
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil
	}
	return records
}

func savePlanningObservationRecords(state session.State, records []planningObservationRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return state.Set(stateKeyPlanningObservations, string(data))
}

func isPlanningInspectionTool(name string) bool {
	switch name {
	case "bash", "list_files", "find_files", "glob_files", "grep_text", "read_file":
		return true
	default:
		return false
	}
}

func recordPlanningObservationAfterTool(ctx tool.Context, calledTool tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
	if ctx == nil || calledTool == nil || ctx.AgentName() != "organization_agent" || err != nil {
		return nil, err
	}
	if !isPlanningInspectionTool(calledTool.Name()) {
		return nil, nil
	}
	records := loadPlanningObservationRecords(ctx.State())
	records = append(records, planningObservationRecord{
		Tool:   calledTool.Name(),
		Input:  summarizePlanningToolInput(calledTool.Name(), args),
		Output: summarizePlanningToolOutput(calledTool.Name(), result),
	})
	if saveErr := savePlanningObservationRecords(ctx.State(), records); saveErr != nil {
		return nil, saveErr
	}
	if saveErr := ctx.State().Set(stateKeyPlanningToolCalls, len(records)); saveErr != nil {
		return nil, saveErr
	}
	return nil, nil
}

func appendPlanningBudgetReminderBeforeModel(ctx agent.CallbackContext, llmRequest *model.LLMRequest) (*model.LLMResponse, error) {
	if ctx == nil || ctx.AgentName() != "organization_agent" {
		return nil, nil
	}
	if getStateInt(ctx.State(), stateKeyPlanningToolCalls) < maxPlanningInspectionToolCalls {
		return nil, nil
	}
	if llmRequest == nil {
		return nil, nil
	}
	summary := buildPlanningObservationSummary(ctx.State())
	llmRequest.Contents = append(llmRequest.Contents, genai.NewContentFromText(
		"Inspection budget reached. Do not call more tools. Return the final JSON plan now using only the observations below.\n"+summary,
		genai.RoleUser,
	))
	return nil, nil
}

func summarizePlanningToolInput(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		return strings.TrimSpace(stringArg(args, "command"))
	case "list_files", "find_files", "glob_files", "grep_text", "read_file":
		data, err := json.Marshal(args)
		if err != nil {
			return ""
		}
		return string(data)
	default:
		return ""
	}
}

func summarizePlanningToolOutput(toolName string, result map[string]any) string {
	switch toolName {
	case "bash":
		output := strings.TrimSpace(stringArg(result, "stdout"))
		if output == "" {
			output = strings.TrimSpace(stringArg(result, "stderr"))
		}
		return output
	default:
		data, err := json.Marshal(result)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

func getStateInt(state session.State, key string) int {
	if state == nil {
		return 0
	}
	value, err := state.Get(key)
	if err != nil || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func stringArg(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func intArg(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	value, ok := values[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func trimForObservation(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "...(truncated)"
}

func validateCommandPlan(plan organizationPlan) []string {
	issues := make([]string, 0)
	for _, command := range plan.Commands {
		normalized := strings.ToLower(strings.TrimSpace(command))
		switch {
		case normalized == "":
			issues = append(issues, "shell command is empty")
		case strings.Contains(normalized, " rm "),
			strings.HasPrefix(normalized, "rm "),
			strings.Contains(normalized, " -delete"),
			strings.Contains(normalized, "find -delete"),
			strings.Contains(normalized, " xargs rm"),
			strings.Contains(normalized, "chmod "),
			strings.Contains(normalized, "chown "),
			strings.Contains(normalized, "sudo "),
			strings.Contains(normalized, "mkfs"),
			strings.Contains(normalized, " dd "):
			issues = append(issues, fmt.Sprintf("unsafe shell command in plan: %s", command))
		}
	}
	return dedupeStrings(issues)
}

func cleanRelativePlanPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return ""
	}
	return cleaned
}

func validateOrganizationPlan(plan organizationPlan, inventory organizationInventory) []string {
	var issues []string
	if len(plan.Moves) == 0 {
		return nil
	}

	filesByPath := make(map[string]inventoryFile, len(inventory.Files))
	for _, file := range inventory.Files {
		filesByPath[file.Path] = file
	}

	seenSrc := make(map[string]bool)
	seenDst := make(map[string]bool)
	for _, move := range plan.Moves {
		if _, ok := filesByPath[move.Src]; !ok {
			issues = append(issues, fmt.Sprintf("source file not found in inventory: %s", move.Src))
		}
		if move.Dst == "" {
			issues = append(issues, fmt.Sprintf("destination path is empty for %s", move.Src))
		}
		if strings.Contains(filepath.Base(move.Dst), "..") {
			issues = append(issues, fmt.Sprintf("destination path is invalid: %s", move.Dst))
		}
		if filepath.Base(move.Src) != filepath.Base(move.Dst) {
			issues = append(issues, fmt.Sprintf("renaming is not allowed: %s -> %s", move.Src, move.Dst))
		}
		if seenSrc[move.Src] {
			issues = append(issues, fmt.Sprintf("duplicate source file in plan: %s", move.Src))
		}
		if seenDst[move.Dst] {
			issues = append(issues, fmt.Sprintf("multiple files target the same destination: %s", move.Dst))
		}
		seenSrc[move.Src] = true
		seenDst[move.Dst] = true
	}
	return dedupeStrings(issues)
}

func mergeReviewWithValidation(review planReview, issues []string) planReview {
	review.Issues = dedupeStrings(append(review.Issues, issues...))
	review.Warnings = dedupeStrings(review.Warnings)
	review.Approved = len(review.Issues) == 0
	return review
}

func dedupeStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func formatPlanForConfirmation(plan organizationPlan, review planReview) string {
	var builder strings.Builder
	builder.WriteString("Plan ready. Review the proposed execution below and reply with yes to execute, no to cancel, or send a revised rule to regenerate the plan.\n\n")
	if plan.Summary != "" {
		builder.WriteString(plan.Summary)
		builder.WriteString("\n\n")
	}
	if len(plan.Commands) > 0 {
		builder.WriteString(fmt.Sprintf("Planned shell commands: %d\n", len(plan.Commands)))
		for _, note := range plan.Notes {
			builder.WriteString("- ")
			builder.WriteString(note)
			builder.WriteString("\n")
		}
		builder.WriteString("\nCommands:\n")
		for _, command := range plan.Commands {
			builder.WriteString("- ")
			builder.WriteString(command)
			builder.WriteString("\n")
		}
	} else {
		builder.WriteString(fmt.Sprintf("Planned moves: %d\n", len(plan.Moves)))
		for _, move := range plan.Moves {
			builder.WriteString("- ")
			builder.WriteString(move.Src)
			builder.WriteString(" -> ")
			builder.WriteString(move.Dst)
			if move.Reason != "" {
				builder.WriteString(" (")
				builder.WriteString(move.Reason)
				builder.WriteString(")")
			}
			builder.WriteString("\n")
		}
	}
	if len(review.Warnings) > 0 {
		builder.WriteString("\nWarnings:\n")
		for _, warning := range review.Warnings {
			builder.WriteString("- ")
			builder.WriteString(warning)
			builder.WriteString("\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func formatPlanIssues(review planReview) string {
	var builder strings.Builder
	builder.WriteString("The plan was not approved. Update the organization rule and I will regenerate it.\n")
	for _, issue := range review.Issues {
		builder.WriteString("- ")
		builder.WriteString(issue)
		builder.WriteString("\n")
	}
	if len(review.Warnings) > 0 {
		builder.WriteString("\nWarnings:\n")
		for _, warning := range review.Warnings {
			builder.WriteString("- ")
			builder.WriteString(warning)
			builder.WriteString("\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func initializePlanTodos(state session.State, plan organizationPlan) (todotool.WriteTodoResult, error) {
	todos := make([]todotool.TodoItem, 0, len(plan.Directories)+len(plan.Moves)+len(plan.Commands))
	for _, dir := range plan.Directories {
		todos = append(todos, todotool.TodoItem{
			Content:    "Create directory: " + dir,
			Status:     "pending",
			ActiveForm: "Creating directory " + dir,
		})
	}
	for _, move := range plan.Moves {
		todos = append(todos, todotool.TodoItem{
			Content:    "Move file: " + move.Src + " -> " + move.Dst,
			Status:     "pending",
			ActiveForm: "Moving " + move.Src + " -> " + move.Dst,
		})
	}
	for i := range plan.Commands {
		todos = append(todos, todotool.TodoItem{
			Content:    fmt.Sprintf("Run shell command %d", i+1),
			Status:     "pending",
			ActiveForm: fmt.Sprintf("Running shell command %d", i+1),
		})
	}
	return todotool.ReplaceTodos(state, todos)
}

func clearTodoState(state session.State) (todotool.WriteTodoResult, error) {
	return todotool.ClearTodos(state)
}

func executeOrganizationPlan(invocationID string, state session.State, root string, plan organizationPlan, moveTools []tool.Tool, bashTool tool.Tool) (executionResult, []*session.Event, error) {
	plan = normalizePlan(plan)
	if err := movetool.ClearLog(); err != nil {
		return executionResult{}, nil, fmt.Errorf("clear operation log: %w", err)
	}

	moveTool, createDirTool := lookupMoveTools(moveTools)
	result := executionResult{}
	steps := buildExecutionSteps(root, plan, moveTool, createDirTool, bashTool)
	todos := buildExecutionTodos(steps)
	todoEvents := make([]*session.Event, 0, 1+len(steps)*2)

	initialTodo, err := todotool.ReplaceTodos(state, todos)
	if err != nil {
		return executionResult{}, nil, fmt.Errorf("initialize todo list: %w", err)
	}
	todoEvents = append(todoEvents, todoResultEvent(invocationID, initialTodo))

	if len(steps) == 0 {
		if err := todotool.EnsureAllCompleted(state); err != nil {
			return executionResult{}, todoEvents, err
		}
		result.TodoList = initialTodo.TodoList
		return result, todoEvents, nil
	}

	for i, step := range steps {
		inProgress, err := setTodoStatus(state, todos, i, "in_progress")
		if err != nil {
			return result, todoEvents, fmt.Errorf("set todo in_progress for step %d: %w", i+1, err)
		}
		todos = inProgress.Todos
		todoEvents = append(todoEvents, todoResultEvent(invocationID, inProgress))

		if err := step.Run(); err != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("%s: %v", step.Label, err))
			result.TodoList = inProgress.TodoList
			return result, todoEvents, fmt.Errorf("step %d failed: %w", i+1, err)
		}

		completed, err := setTodoStatus(state, todos, i, "completed")
		if err != nil {
			return result, todoEvents, fmt.Errorf("set todo completed for step %d: %w", i+1, err)
		}
		todos = completed.Todos
		todoEvents = append(todoEvents, todoResultEvent(invocationID, completed))

		switch {
		case strings.HasPrefix(step.Label, "Create directory: "):
			result.CreatedDir = append(result.CreatedDir, strings.TrimPrefix(step.Label, "Create directory: "))
		case strings.HasPrefix(step.Label, "Move file: "):
			mapping := strings.TrimPrefix(step.Label, "Move file: ")
			src, dst, ok := strings.Cut(mapping, " -> ")
			if ok {
				result.Moved = append(result.Moved, organizationMove{Src: src, Dst: dst})
			}
		case strings.HasPrefix(step.Label, "Run shell command "):
			result.Commands = append(result.Commands, step.ActiveForm)
		}
	}

	if err := todotool.EnsureAllCompleted(state); err != nil {
		return result, todoEvents, fmt.Errorf("todo state machine validation failed: %w", err)
	}

	loadedTodos, err := todotool.LoadTodos(state)
	if err != nil {
		return result, todoEvents, fmt.Errorf("load final todo list: %w", err)
	}
	result.TodoList = renderTodoListForSummary(loadedTodos)
	return result, todoEvents, nil
}

func buildExecutionSteps(root string, plan organizationPlan, moveTool, createDirTool, bashTool tool.Tool) []executionStep {
	steps := make([]executionStep, 0, len(plan.Directories)+len(plan.Moves)+len(plan.Commands))
	for _, dir := range plan.Directories {
		relativeDir := dir
		absDir := filepath.Join(root, filepath.FromSlash(dir))
		steps = append(steps, executionStep{
			Label:      "Create directory: " + relativeDir,
			ActiveForm: "Creating directory " + relativeDir,
			Run: func() error {
				return createDirectory(absDir, createDirTool)
			},
		})
	}
	for _, move := range plan.Moves {
		moveItem := move
		src := filepath.Join(root, filepath.FromSlash(moveItem.Src))
		dst := filepath.Join(root, filepath.FromSlash(moveItem.Dst))
		steps = append(steps, executionStep{
			Label:      "Move file: " + moveItem.Src + " -> " + moveItem.Dst,
			ActiveForm: "Moving " + moveItem.Src + " -> " + moveItem.Dst,
			Run: func() error {
				return movePathWithTool(src, dst, moveTool)
			},
		})
	}
	for i, command := range plan.Commands {
		commandText := command
		label := fmt.Sprintf("Run shell command %d", i+1)
		steps = append(steps, executionStep{
			Label:      label,
			ActiveForm: commandText,
			Run: func() error {
				_, err := runBashToolCommand(root, bashTool, commandText)
				return err
			},
		})
	}
	return steps
}

func buildExecutionTodos(steps []executionStep) []todotool.TodoItem {
	todos := make([]todotool.TodoItem, 0, len(steps))
	for _, step := range steps {
		todos = append(todos, todotool.TodoItem{
			Content:    step.Label,
			Status:     "pending",
			ActiveForm: step.ActiveForm,
		})
	}
	return todos
}

func setTodoStatus(state session.State, todos []todotool.TodoItem, activeIndex int, status string) (todotool.WriteTodoResult, error) {
	next := make([]todotool.TodoItem, len(todos))
	copy(next, todos)
	for i := range next {
		switch {
		case i < activeIndex && next[i].Status != "completed":
			next[i].Status = "completed"
		case i == activeIndex:
			next[i].Status = status
		case i > activeIndex && next[i].Status == "completed":
			next[i].Status = "pending"
		case i > activeIndex:
			next[i].Status = "pending"
		}
	}
	return todotool.ReplaceTodos(state, next)
}

func todoResultEvent(invocationID string, result todotool.WriteTodoResult) *session.Event {
	response := map[string]any{
		"todo_list":   result.TodoList,
		"total_items": result.TotalItems,
		"counts":      result.Counts,
	}
	event := session.NewEvent(invocationID)
	event.Content = &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name:     "write_todo",
					Response: response,
				},
			},
		},
	}
	return event
}

func renderTodoListForSummary(todos []todotool.TodoItem) string {
	if len(todos) == 0 {
		return ""
	}
	lines := make([]string, 0, len(todos))
	for _, item := range todos {
		lines = append(lines, item.Status+": "+item.Content)
	}
	return strings.Join(lines, "\n")
}

func lookupMoveTools(tools []tool.Tool) (tool.Tool, tool.Tool) {
	var moveTool, createDirTool tool.Tool
	for _, candidate := range tools {
		switch candidate.Name() {
		case "move_file":
			moveTool = candidate
		case "create_dir":
			createDirTool = candidate
		}
	}
	return moveTool, createDirTool
}

func createDirectory(path string, createDirTool tool.Tool) error {
	logToolCall("executor_agent", "create_dir", map[string]any{"path": path})
	if createDirTool == nil {
		err := os.MkdirAll(path, 0o755)
		logToolResponse("executor_agent", "create_dir", map[string]any{"created": err == nil, "path": path, "error": errorString(err)})
		return err
	}

	runner, ok := createDirTool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		return fmt.Errorf("create_dir tool is not runnable")
	}
	result, err := runner.Run(nil, map[string]any{"path": path})
	logToolResponse("executor_agent", "create_dir", mergeToolLogResult(result, err))
	return err
}

func movePathWithTool(src, dst string, moveTool tool.Tool) error {
	logToolCall("executor_agent", "move_file", map[string]any{"src": src, "dst": dst})
	if moveTool == nil {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			logToolResponse("executor_agent", "move_file", map[string]any{"moved": false, "src": src, "dst": dst, "error": err.Error()})
			return err
		}
		err := os.Rename(src, dst)
		logToolResponse("executor_agent", "move_file", map[string]any{"moved": err == nil, "src": src, "dst": dst, "error": errorString(err)})
		return err
	}

	runner, ok := moveTool.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		return fmt.Errorf("move_file tool is not runnable")
	}
	result, err := runner.Run(nil, map[string]any{"src": src, "dst": dst})
	logToolResponse("executor_agent", "move_file", mergeToolLogResult(result, err))
	return err
}

func formatExecutionResult(result executionResult) string {
	var builder strings.Builder
	switch {
	case len(result.Commands) > 0:
		builder.WriteString(fmt.Sprintf("Execution complete. Ran %d shell command(s).", len(result.Commands)))
	case len(result.Moved) > 0:
		builder.WriteString(fmt.Sprintf("Execution complete. Moved %d file(s).", len(result.Moved)))
	default:
		builder.WriteString("Execution complete.")
	}
	if len(result.CreatedDir) > 0 {
		builder.WriteString(fmt.Sprintf(" Created %d directorie(s).", len(result.CreatedDir)))
	}
	if len(result.Failures) == 0 {
		return builder.String()
	}

	builder.WriteString("\n\nFailures:\n")
	for _, failure := range result.Failures {
		builder.WriteString("- ")
		builder.WriteString(failure)
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func isExecutionConfirmed(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch normalized {
	case "y", "yes", "ok", "okay", "go", "go ahead", "confirm", "confirmed", "execute", "run", "是", "好", "好的", "确认", "执行", "开始":
		return true
	default:
		return false
	}
}

func isExecutionCancelled(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch normalized {
	case "n", "no", "cancel", "stop", "abort", "算了", "取消", "不要", "停止":
		return true
	default:
		return false
	}
}

func clearedWorkflowStateDelta() map[string]any {
	return map[string]any{
		stateKeyAwaitingConfirmation: "",
		stateKeyOrganizePending:      "false",
		stateKeyPendingField:         "",
		stateKeyPlanningToolCalls:    0,
		stateKeyPlanningObservations: "",
		stateKeyExecutionPlan:        "",
		stateKeyExecutionReview:      "",
	}
}

func logToolCall(agentName, name string, args map[string]any) {
	log.Printf("[TOOL CALL][agent=%s][tool=%s] %s", agentName, name, truncateStructuredForLog(args, 500))
}

func logToolResponse(agentName, name string, result map[string]any) {
	log.Printf("[TOOL RESPONSE][agent=%s][tool=%s] %s", agentName, name, truncateStructuredForLog(result, 500))
}

func truncateStructuredForLog(value any, max int) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	text := string(data)
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "...(truncated)"
}

func mergeToolLogResult(result map[string]any, err error) map[string]any {
	if result == nil {
		result = map[string]any{}
	}
	if err != nil {
		result["error"] = err.Error()
	}
	return result
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
