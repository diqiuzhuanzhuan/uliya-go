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
		t.Fatalf("expected 5 todo events, got %d", len(events))
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
