package main

import (
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loong/uliya-go/tools/todotool"
)

type workflowTestState struct {
	values map[string]any
}

func newWorkflowTestState() *workflowTestState {
	return &workflowTestState{values: map[string]any{}}
}

func (s *workflowTestState) Get(key string) (any, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, nil
	}
	return value, nil
}

func (s *workflowTestState) Set(key string, value any) error {
	s.values[key] = value
	return nil
}

func (s *workflowTestState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, value := range s.values {
			if !yield(key, value) {
				return
			}
		}
	}
}

func TestExecuteOrganizationPlanCompletesAllTodos(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "invoice.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := newWorkflowTestState()
	plan := organizationPlan{
		Directories: []string{"Docs"},
		Moves: []organizationMove{
			{Src: "invoice.txt", Dst: "Docs/invoice.txt"},
		},
	}

	result, events, err := executeOrganizationPlan("inv-1", state, root, plan, nil, nil)
	if err != nil {
		t.Fatalf("executeOrganizationPlan() error = %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 todo events (legacy path), got %d", len(events))
	}
	if _, err := os.Stat(filepath.Join(root, "Docs", "invoice.txt")); err != nil {
		t.Fatalf("expected moved file, stat error = %v", err)
	}
	if len(result.Moved) != 1 || result.Moved[0].Dst != "Docs/invoice.txt" {
		t.Fatalf("unexpected move result: %#v", result.Moved)
	}
	if err := todotool.EnsureAllCompleted(state); err != nil {
		t.Fatalf("EnsureAllCompleted() error = %v", err)
	}
}

func TestExecuteOrganizationPlanWithFullPhaseTodos(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "invoice.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	state := newWorkflowTestState()
	plan := organizationPlan{
		Directories: []string{"Docs"},
		Moves: []organizationMove{
			{Src: "invoice.txt", Dst: "Docs/invoice.txt"},
		},
	}

	// Simulate the workflow setup: initializeFullPhaseTodos is called before execution.
	if _, err := initializeFullPhaseTodos(state, plan); err != nil {
		t.Fatalf("initializeFullPhaseTodos() error = %v", err)
	}

	result, events, err := executeOrganizationPlan("inv-full", state, root, plan, nil, nil)
	if err != nil {
		t.Fatalf("executeOrganizationPlan() error = %v", err)
	}
	// Events: confirm completed, create_dir in_progress, create_dir completed,
	// move in_progress, move completed, verify in_progress, verify completed = 7.
	if len(events) != 7 {
		t.Fatalf("expected 7 todo events (full-phase path), got %d", len(events))
	}
	if _, err := os.Stat(filepath.Join(root, "Docs", "invoice.txt")); err != nil {
		t.Fatalf("expected moved file, stat error = %v", err)
	}
	if len(result.Moved) != 1 || result.Moved[0].Dst != "Docs/invoice.txt" {
		t.Fatalf("unexpected move result: %#v", result.Moved)
	}
	if len(result.UnmovedFiles) != 0 {
		t.Fatalf("expected no unmoved files after organizing, got %v", result.UnmovedFiles)
	}
	if err := todotool.EnsureAllCompleted(state); err != nil {
		t.Fatalf("EnsureAllCompleted() error = %v", err)
	}

	todos, err := todotool.LoadTodos(state)
	if err != nil {
		t.Fatalf("LoadTodos() error = %v", err)
	}
	if len(todos) != 5 {
		t.Fatalf("expected 5 todos in full-phase list, got %d", len(todos))
	}
	for _, item := range todos {
		if item.Status != "completed" {
			t.Fatalf("expected all todos completed, got %q with status %q", item.Content, item.Status)
		}
	}
}

func TestExecuteOrganizationPlanStopsOnIncompleteTodo(t *testing.T) {
	root := t.TempDir()
	state := newWorkflowTestState()
	plan := organizationPlan{
		Moves: []organizationMove{
			{Src: "missing.txt", Dst: "Docs/missing.txt"},
		},
	}

	_, events, err := executeOrganizationPlan("inv-2", state, root, plan, nil, nil)
	if err == nil {
		t.Fatal("expected execution error")
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 todo events, got %d", len(events))
	}

	todos, loadErr := todotool.LoadTodos(state)
	if loadErr != nil {
		t.Fatalf("LoadTodos() error = %v", loadErr)
	}
	if len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("expected single in_progress todo, got %#v", todos)
	}
}

func TestResolveOrganizationPathFollowsSymlinkedDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	realRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(realRoot, "invoice.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	linkPath := filepath.Join(t.TempDir(), "Downloads")
	if err := os.Symlink(realRoot, linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	absRoot, _, err := resolveOrganizationPath(repoRoot, linkPath)
	if err != nil {
		t.Fatalf("resolveOrganizationPath() error = %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if absRoot != wantRoot {
		t.Fatalf("expected resolved root %q, got %q", wantRoot, absRoot)
	}

	inventory, err := collectOrganizationInventory(absRoot)
	if err != nil {
		t.Fatalf("collectOrganizationInventory() error = %v", err)
	}
	if len(inventory.Files) != 1 || inventory.Files[0].Path != "invoice.txt" {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
}

func TestValidateCommandPlanRejectsDestructiveCommands(t *testing.T) {
	plan := organizationPlan{
		Commands: []string{
			`find . -type f -delete`,
			`rm -rf .`,
			`chmod -R 777 .`,
		},
	}

	issues := validateCommandPlan(plan)
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %#v", issues)
	}
}

func TestValidateDiscoveryCommandRejectsGNUAndMutatingCommands(t *testing.T) {
	cases := []string{
		`find . -type f -printf '%p\n'`,
		`mv a b`,
		`cat secret.txt`,
	}

	for _, command := range cases {
		if err := validateDiscoveryCommand(command); err == nil {
			t.Fatalf("expected discovery command to be rejected: %q", command)
		}
	}
}

func TestValidateDiscoveryCommandAllowsMacOSMetadataCommands(t *testing.T) {
	cases := []string{
		`find . -type f | sed -n '1,20p'`,
		`stat -f '%N %z %Sm' * 2>/dev/null`,
		`find . -type f | awk -F. 'NF>1{print $NF}' | sort | uniq -c`,
	}

	for _, command := range cases {
		if err := validateDiscoveryCommand(command); err != nil {
			t.Fatalf("expected discovery command to be allowed: %q (%v)", command, err)
		}
	}
}

func TestIntentUsesExtensionGrouping(t *testing.T) {
	cases := []string{
		"organize by extension",
		"group files by file type",
		"按扩展名整理",
		"按文件类型分类",
	}
	for _, input := range cases {
		if !intentUsesExtensionGrouping(input) {
			t.Fatalf("expected extension intent for %q", input)
		}
	}
	if intentUsesExtensionGrouping("organize by modified date") {
		t.Fatal("did not expect date-based intent to be treated as extension grouping")
	}
}

func TestIsExtensionSummaryCommand(t *testing.T) {
	if !isExtensionSummaryCommand(`find . -type f | awk -F. 'NF>1{print tolower($NF)} NF==1{print "[no extension]"}' | sort | uniq -c`) {
		t.Fatal("expected extension summary command to be accepted")
	}
	if isExtensionSummaryCommand(`find . -type f | sort`) {
		t.Fatal("did not expect full filename listing to be treated as an extension summary")
	}
}

func TestBuildPlanningObservationSummaryUsesRecordedToolOutputs(t *testing.T) {
	state := newWorkflowTestState()
	records := []planningObservationRecord{
		{Tool: "bash", Input: "find . -type f | sed -n '1,10p'", Output: "invoice.txt\nphoto.jpg\n"},
		{Tool: "bash", Input: "find . -type f | awk -F. 'NF>1{print $NF}' | sort | uniq -c", Output: "1 jpg\n1 txt\n"},
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := state.Set(stateKeyPlanningObservations, string(data)); err != nil {
		t.Fatalf("state.Set() error = %v", err)
	}
	if err := state.Set(stateKeyPlanningToolCalls, 2); err != nil {
		t.Fatalf("state.Set() error = %v", err)
	}

	summary := buildPlanningObservationSummary(state)
	if summary == "" {
		t.Fatal("expected non-empty planning observation summary")
	}
	if !strings.Contains(summary, "find . -type f") || !strings.Contains(summary, "1 jpg") {
		t.Fatalf("unexpected planning observation summary: %q", summary)
	}
}

func TestParseJSONBlockExtractsEmbeddedJSONObject(t *testing.T) {
	raw := `好的，最终计划如下。

` + "```json" + `
{"summary":"sort docs","directories":["Docs"],"moves":[{"src":"invoice.txt","dst":"Docs/invoice.txt"}]}
` + "```" + `

确认后我会执行。`

	plan, err := parseJSONBlock[organizationPlan](raw)
	if err != nil {
		t.Fatalf("parseJSONBlock() error = %v", err)
	}
	if plan.Summary != "sort docs" {
		t.Fatalf("unexpected summary: %#v", plan)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].Dst != "Docs/invoice.txt" {
		t.Fatalf("unexpected moves: %#v", plan.Moves)
	}
}

func TestParseIntakeAnalysisAcceptsWrappedJSON(t *testing.T) {
	raw := `Here is the analysis:
{"relevant":true,"path":"~/Downloads","intent":"organize by file type","use_current_workspace":false}
Thanks.`

	analysis, ok := parseIntakeAnalysis(raw)
	if !ok {
		t.Fatal("expected parseIntakeAnalysis() to succeed")
	}
	if !analysis.Relevant || analysis.Path != "~/Downloads" || analysis.Intent != "organize by file type" {
		t.Fatalf("unexpected analysis: %#v", analysis)
	}
}

func TestFormatPlanForConfirmationChinese(t *testing.T) {
	plan := organizationPlan{
		Summary: "将文件按扩展名整理到不同目录。",
		Moves: []organizationMove{
			{Src: "a.txt", Dst: "txt/a.txt", Reason: "按扩展名归档"},
		},
	}

	got := formatPlanForConfirmation(plan, planReview{}, true)
	if !strings.Contains(got, "计划已经生成") {
		t.Fatalf("expected Chinese confirmation intro, got %q", got)
	}
	if !strings.Contains(got, "计划移动文件：1") {
		t.Fatalf("expected Chinese move count, got %q", got)
	}
}

func TestFormatExecutionResultChinese(t *testing.T) {
	got := formatExecutionResult(executionResult{
		Moved:      []organizationMove{{Src: "a.txt", Dst: "txt/a.txt"}},
		CreatedDir: []string{"txt"},
	}, true)
	if !strings.Contains(got, "执行完成，共移动 1 个文件。") {
		t.Fatalf("expected Chinese execution summary, got %q", got)
	}
	if !strings.Contains(got, "校验结果：所有文件都已整理完成。") {
		t.Fatalf("expected Chinese verification summary, got %q", got)
	}
}

func TestFormatPlanIssuesChinese(t *testing.T) {
	got := formatPlanIssues(planReview{
		Issues: []string{"source file not found in inventory: a.txt"},
	}, true)
	if !strings.Contains(got, "当前计划未通过校验") {
		t.Fatalf("expected Chinese rejection intro, got %q", got)
	}
	if !strings.Contains(got, "在目录清单中找不到源文件：a.txt") {
		t.Fatalf("expected localized issue, got %q", got)
	}
}

func TestLocalizePlanLanguageChineseDirectories(t *testing.T) {
	plan := organizationPlan{
		Directories: []string{"Docs/2026-04", "by_extension/jpg"},
		Moves: []organizationMove{
			{Src: "invoice.txt", Dst: "Docs/2026-04/invoice.txt"},
		},
		Commands: []string{
			`mkdir -p "by_extension/jpg" && mv "photo.jpg" "by_extension/jpg/photo.jpg"`,
		},
	}

	got := localizePlanLanguage(plan, true)
	if got.Directories[0] != "文档/2026-04" || got.Directories[1] != "按扩展名/jpg" {
		t.Fatalf("unexpected localized directories: %#v", got.Directories)
	}
	if got.Moves[0].Dst != "文档/2026-04/invoice.txt" {
		t.Fatalf("unexpected localized move destination: %#v", got.Moves)
	}
	if !strings.Contains(got.Commands[0], "按扩展名/jpg") {
		t.Fatalf("expected localized command path, got %q", got.Commands[0])
	}
}
