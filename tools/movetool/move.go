package movetool

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const logFileName = ".uliya_ops.json"

// Operation records a single file-system change for undo purposes.
type Operation struct {
	Op        string    `json:"op"`
	Src       string    `json:"src,omitempty"`
	Dst       string    `json:"dst,omitempty"`
	Path      string    `json:"path,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type operationLog struct {
	Operations []Operation `json:"operations"`
}

func logFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, logFileName), nil
}

// AppendOperation appends an operation to the persistent log.
func AppendOperation(op Operation) error {
	op.Timestamp = time.Now()
	path, err := logFilePath()
	if err != nil {
		return err
	}
	var lg operationLog
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &lg)
	}
	lg.Operations = append(lg.Operations, op)
	data, err := json.MarshalIndent(lg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadOperations reads the operation log from disk.
func LoadOperations() ([]Operation, error) {
	path, err := logFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lg operationLog
	if err := json.Unmarshal(data, &lg); err != nil {
		return nil, err
	}
	return lg.Operations, nil
}

// ClearLog removes the operation log file.
func ClearLog() error {
	path, err := logFilePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// New returns both file-operation tools.
func New() []tool.Tool {
	return []tool.Tool{&MoveFileTool{}, &CreateDirTool{}}
}

// ---- MoveFileTool ----

// MoveFileTool moves or renames a file or directory and records the operation.
type MoveFileTool struct{}

func (t *MoveFileTool) Name() string { return "move_file" }

func (t *MoveFileTool) Description() string {
	return "Move or rename a file or directory. Creates the destination parent directory if needed. Records the operation for undo."
}

func (t *MoveFileTool) IsLongRunning() bool { return false }

func (t *MoveFileTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		ParametersJsonSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"src": map[string]any{
					"type":        "string",
					"description": "Absolute path of the file or directory to move.",
				},
				"dst": map[string]any{
					"type":        "string",
					"description": "Absolute destination path (including filename).",
				},
			},
			"required":             []string{"src", "dst"},
			"additionalProperties": false,
		},
	}
}

func (t *MoveFileTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
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

func (t *MoveFileTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}
	var input struct {
		Src string `json:"src"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("decode args: %w", err)
	}
	if input.Src == "" || input.Dst == "" {
		return nil, fmt.Errorf("src and dst are required")
	}

	if err := os.MkdirAll(filepath.Dir(input.Dst), 0o755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}
	if err := movePath(input.Src, input.Dst); err != nil {
		return nil, err
	}
	_ = AppendOperation(Operation{Op: "move", Src: input.Src, Dst: input.Dst})
	return map[string]any{"moved": true, "src": input.Src, "dst": input.Dst}, nil
}

// movePath attempts os.Rename and falls back to copy+delete for cross-device moves.
func movePath(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyPath(src, dst); err != nil {
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	return os.RemoveAll(src)
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst, info.Mode())
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyPath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// ---- CreateDirTool ----

// CreateDirTool creates a directory (with parents) and records the operation.
type CreateDirTool struct{}

func (t *CreateDirTool) Name() string { return "create_dir" }

func (t *CreateDirTool) Description() string {
	return "Create a directory and any missing parent directories. Records the operation for undo."
}

func (t *CreateDirTool) IsLongRunning() bool { return false }

func (t *CreateDirTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		ParametersJsonSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path of the directory to create.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	}
}

func (t *CreateDirTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
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

func (t *CreateDirTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("decode args: %w", err)
	}
	if input.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if err := os.MkdirAll(input.Path, 0o755); err != nil {
		return nil, fmt.Errorf("create directory: %w", err)
	}
	_ = AppendOperation(Operation{Op: "create_dir", Path: input.Path})
	return map[string]any{"created": true, "path": input.Path}, nil
}
