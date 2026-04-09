package todotool

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const (
	stateKeyTodoItems           = "todo_items"
	stateKeyTodoList            = "todo_list"
	stateKeyTodoRefreshReminder = "temp:todo_refresh_reminder"
)

type WriteTodoTool struct{}

type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status,omitempty"`
	ActiveForm string `json:"activeForm,omitempty"`
}

type writeTodoArgs struct {
	Todos []TodoItem `json:"todos"`
}

type writeTodoResult struct {
	Todos      []TodoItem     `json:"todos"`
	TodoList   string         `json:"todo_list"`
	Counts     map[string]int `json:"counts"`
	TotalItems int            `json:"total_items"`
}

func New() []tool.Tool {
	return []tool.Tool{&WriteTodoTool{}}
}

func (t *WriteTodoTool) Name() string { return "write_todo" }

func (t *WriteTodoTool) Description() string {
	return "Replace the current todo list for this session. Use this only after the task is concrete enough to execute, especially for file-organization work where you should already know the target path and the organization strategy, or have already inspected the path and formed a concrete plan. Do not use this at the start of a vague request, do not use it before the user has identified what directory or file should be organized, and do not use it just because a task might become multi-step. Send the full current list each time, not just the changed item. Valid statuses are pending, in_progress, and completed."
}

func (t *WriteTodoTool) IsLongRunning() bool { return false }

func (t *WriteTodoTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		ParametersJsonSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":        "array",
					"description": "The full todo list in the current desired order. Only send this after the task is concrete enough to execute. Rewrite the whole list every time you update progress.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{
								"type":        "string",
								"description": "The todo item text shown when the task is pending or completed.",
							},
							"status": map[string]any{
								"type":        "string",
								"description": "One of pending, in_progress, or completed.",
								"enum":        []string{"pending", "in_progress", "completed"},
							},
							"activeForm": map[string]any{
								"type":        "string",
								"description": "Optional present-progress phrasing for the currently active item, for example \"Sorting screenshots into folders\".",
							},
						},
						"required":             []string{"content", "status"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		},
	}
}

func (t *WriteTodoTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if req == nil {
		return fmt.Errorf("request is nil")
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

func (t *WriteTodoTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, fmt.Errorf("tool context is required")
	}
	input, err := decodeArgs(args)
	if err != nil {
		return nil, err
	}
	result, err := writeTodos(ctx.State(), input.Todos)
	if err != nil {
		return nil, err
	}
	return toMap(result)
}

func decodeArgs(raw any) (writeTodoArgs, error) {
	if raw == nil {
		return writeTodoArgs{}, fmt.Errorf("args are required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return writeTodoArgs{}, fmt.Errorf("marshal args: %w", err)
	}
	var input writeTodoArgs
	if err := json.Unmarshal(data, &input); err != nil {
		return writeTodoArgs{}, fmt.Errorf("decode args: %w", err)
	}
	return input, nil
}

func writeTodos(state session.State, todos []TodoItem) (writeTodoResult, error) {
	if state == nil {
		return writeTodoResult{}, fmt.Errorf("session state is not available")
	}

	normalized := make([]TodoItem, 0, len(todos))
	counts := map[string]int{
		"pending":     0,
		"in_progress": 0,
		"completed":   0,
	}

	for i, item := range todos {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return writeTodoResult{}, fmt.Errorf("todos[%d].content is required", i)
		}

		status := strings.TrimSpace(item.Status)
		if status == "" {
			status = "pending"
		}
		switch status {
		case "pending", "in_progress", "completed":
		default:
			return writeTodoResult{}, fmt.Errorf("todos[%d].status must be pending, in_progress, or completed", i)
		}

		activeForm := strings.TrimSpace(item.ActiveForm)
		if status == "in_progress" && activeForm == "" {
			activeForm = content
		}

		normalized = append(normalized, TodoItem{
			Content:    content,
			Status:     status,
			ActiveForm: activeForm,
		})
		counts[status]++
	}

	if counts["in_progress"] > 1 {
		return writeTodoResult{}, fmt.Errorf("only one todo item can be in_progress at a time")
	}

	rendered := renderTodoList(normalized)
	if err := state.Set(stateKeyTodoItems, normalized); err != nil {
		return writeTodoResult{}, fmt.Errorf("save todo_items: %w", err)
	}
	if err := state.Set(stateKeyTodoList, rendered); err != nil {
		return writeTodoResult{}, fmt.Errorf("save todo_list: %w", err)
	}
	if err := clearRefreshReminder(state); err != nil {
		return writeTodoResult{}, fmt.Errorf("clear todo refresh reminder: %w", err)
	}

	return writeTodoResult{
		Todos:      normalized,
		TodoList:   rendered,
		Counts:     counts,
		TotalItems: len(normalized),
	}, nil
}

func renderTodoList(todos []TodoItem) string {
	if len(todos) == 0 {
		return "当前没有待办事项。"
	}

	lines := make([]string, 0, len(todos))
	for i, item := range todos {
		line := item.Content
		switch item.Status {
		case "in_progress":
			if item.ActiveForm != "" {
				line = item.ActiveForm
			}
			line = fmt.Sprintf("%d. [-] %s", i+1, line)
		case "completed":
			line = fmt.Sprintf("%d. [x] %s", i+1, line)
		default:
			line = fmt.Sprintf("%d. [ ] %s", i+1, line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func LoadTodos(state session.ReadonlyState) ([]TodoItem, error) {
	if state == nil {
		return nil, fmt.Errorf("session state is not available")
	}
	value, err := state.Get(stateKeyTodoItems)
	if err != nil {
		if err == session.ErrStateKeyNotExist {
			return nil, nil
		}
		return nil, fmt.Errorf("load todo_items: %w", err)
	}
	if value == nil {
		return nil, nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal todo_items: %w", err)
	}
	var items []TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode todo_items: %w", err)
	}
	return items, nil
}

func ActiveTodo(state session.ReadonlyState) (*TodoItem, error) {
	todos, err := LoadTodos(state)
	if err != nil {
		return nil, err
	}
	for i := range todos {
		if todos[i].Status == "in_progress" {
			item := todos[i]
			return &item, nil
		}
	}
	return nil, nil
}

func MarkRefreshNeeded(state session.State, toolName string) error {
	if state == nil {
		return fmt.Errorf("session state is not available")
	}
	active, err := ActiveTodo(state)
	if err != nil {
		return err
	}
	if active == nil {
		return clearRefreshReminder(state)
	}
	label := active.Content
	if strings.TrimSpace(active.ActiveForm) != "" {
		label = active.ActiveForm
	}
	reminder := fmt.Sprintf("待办状态检查：你刚执行了工具 %s。请先检查当前进行中的待办“%s”是否已经完成；如果完成了，先调用 write_todo 把它更新为 completed，再继续其他操作或回复用户。", toolName, label)
	return state.Set(stateKeyTodoRefreshReminder, reminder)
}

func clearRefreshReminder(state session.State) error {
	return state.Set(stateKeyTodoRefreshReminder, "")
}

func toMap(result writeTodoResult) (map[string]any, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
