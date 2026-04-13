package todotool

import (
	"iter"
	"strings"
	"testing"
)

type testState struct {
	values map[string]any
}

func newTestState() *testState {
	return &testState{values: map[string]any{}}
}

func (s *testState) Get(key string) (any, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, nil
	}
	return value, nil
}

func (s *testState) Set(key string, value any) error {
	s.values[key] = value
	return nil
}

func (s *testState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, value := range s.values {
			if !yield(key, value) {
				return
			}
		}
	}
}

func TestWriteTodosStoresRenderedListInState(t *testing.T) {
	state := newTestState()
	result, err := ReplaceTodos(state, []TodoItem{
		{Content: "Inspect Downloads", Status: "completed"},
		{Content: "Group screenshots", Status: "in_progress", ActiveForm: "Grouping screenshots by project"},
		{Content: "Move archives", Status: "pending"},
	})
	if err != nil {
		t.Fatalf("writeTodos() error = %v", err)
	}

	if result.TotalItems != 3 {
		t.Fatalf("expected 3 items, got %d", result.TotalItems)
	}
	if result.Counts["completed"] != 1 || result.Counts["in_progress"] != 1 || result.Counts["pending"] != 1 {
		t.Fatalf("unexpected counts: %#v", result.Counts)
	}

	rendered, _ := state.Get(stateKeyTodoList)
	text := rendered.(string)
	if !strings.Contains(text, "[x] Inspect Downloads") || !strings.Contains(text, "[-] Grouping screenshots by project") {
		t.Fatalf("unexpected rendered todo list: %q", text)
	}
}

func TestWriteTodosDefaultsPendingStatus(t *testing.T) {
	state := newTestState()
	result, err := ReplaceTodos(state, []TodoItem{
		{Content: "Create folders"},
	})
	if err != nil {
		t.Fatalf("writeTodos() error = %v", err)
	}
	if result.Todos[0].Status != "pending" {
		t.Fatalf("expected pending status, got %#v", result.Todos[0])
	}
}

func TestSnapshotReturnsCurrentTodoState(t *testing.T) {
	state := newTestState()
	if _, err := ReplaceTodos(state, []TodoItem{
		{Content: "Inspect files", Status: "completed"},
		{Content: "Move files", Status: "pending"},
	}); err != nil {
		t.Fatalf("ReplaceTodos() error = %v", err)
	}

	result, err := Snapshot(state)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if result.TotalItems != 2 || result.Counts["completed"] != 1 || result.Counts["pending"] != 1 {
		t.Fatalf("unexpected snapshot result: %#v", result)
	}
	if !strings.Contains(result.TodoList, "[x] Inspect files") || !strings.Contains(result.TodoList, "[ ] Move files") {
		t.Fatalf("unexpected snapshot todo list: %q", result.TodoList)
	}
}

func TestWriteTodosRejectsMultipleInProgressItems(t *testing.T) {
	state := newTestState()
	_, err := ReplaceTodos(state, []TodoItem{
		{Content: "A", Status: "in_progress"},
		{Content: "B", Status: "in_progress"},
	})
	if err == nil {
		t.Fatal("expected error for multiple in_progress items")
	}
	if !strings.Contains(err.Error(), "only one todo item can be in_progress") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteTodosRejectsInvalidStatus(t *testing.T) {
	state := newTestState()
	_, err := ReplaceTodos(state, []TodoItem{
		{Content: "A", Status: "doing"},
	})
	if err == nil {
		t.Fatal("expected invalid status error")
	}
	if !strings.Contains(err.Error(), "must be pending, in_progress, or completed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMarkRefreshNeededSetsReminderForActiveTodo(t *testing.T) {
	state := newTestState()
	if _, err := ReplaceTodos(state, []TodoItem{
		{Content: "Inspect files", Status: "in_progress", ActiveForm: "Inspecting files"},
		{Content: "Move files", Status: "pending"},
	}); err != nil {
		t.Fatalf("writeTodos() error = %v", err)
	}

	if err := MarkRefreshNeeded(state, "list_files"); err != nil {
		t.Fatalf("MarkRefreshNeeded() error = %v", err)
	}

	got, _ := state.Get(stateKeyTodoRefreshReminder)
	text := got.(string)
	if !strings.Contains(text, "list_files") || !strings.Contains(text, "Inspecting files") {
		t.Fatalf("unexpected reminder: %q", text)
	}
}

func TestMarkRefreshNeededClearsReminderWhenNoActiveTodo(t *testing.T) {
	state := newTestState()
	if _, err := ReplaceTodos(state, []TodoItem{
		{Content: "Inspect files", Status: "completed"},
	}); err != nil {
		t.Fatalf("writeTodos() error = %v", err)
	}

	if err := MarkRefreshNeeded(state, "list_files"); err != nil {
		t.Fatalf("MarkRefreshNeeded() error = %v", err)
	}

	got, _ := state.Get(stateKeyTodoRefreshReminder)
	if got.(string) != "" {
		t.Fatalf("expected empty reminder, got %q", got.(string))
	}
}

func TestRefreshReminderReadsCurrentReminder(t *testing.T) {
	state := newTestState()
	if err := state.Set(stateKeyTodoRefreshReminder, "refresh me"); err != nil {
		t.Fatalf("state.Set() error = %v", err)
	}

	reminder, err := RefreshReminder(state)
	if err != nil {
		t.Fatalf("RefreshReminder() error = %v", err)
	}
	if reminder != "refresh me" {
		t.Fatalf("expected reminder to round-trip, got %q", reminder)
	}
}

func TestEnsureAllCompletedRejectsPendingItems(t *testing.T) {
	state := newTestState()
	if _, err := ReplaceTodos(state, []TodoItem{
		{Content: "Inspect files", Status: "completed"},
		{Content: "Move files", Status: "pending"},
	}); err != nil {
		t.Fatalf("ReplaceTodos() error = %v", err)
	}

	err := EnsureAllCompleted(state)
	if err == nil {
		t.Fatal("expected incomplete todo error")
	}
	if !strings.Contains(err.Error(), "Move files") {
		t.Fatalf("unexpected error: %v", err)
	}
}
