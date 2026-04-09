package bashtool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const (
	defaultTimeoutSeconds = 20
	maxTimeoutSeconds     = 120
	defaultMaxOutputBytes = 16 * 1024
)

type Args struct {
	Command        string `json:"command"`
	Workdir        string `json:"workdir,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int    `json:"max_output_bytes,omitempty"`
}

type Result struct {
	Command    string `json:"command"`
	Workdir    string `json:"workdir"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Truncated  bool   `json:"truncated"`
}

type Tool struct {
	rootDir string
}

func New(rootDir string) (*Tool, error) {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve root dir: %w", err)
	}
	return &Tool{rootDir: rootDir}, nil
}

func (t *Tool) Name() string {
	return "bash"
}

func (t *Tool) Description() string {
	return "Run a bash command to inspect or modify files. Use it for commands such as ls, find, rg, cat, sed, pwd, mkdir, cp, mv, and file edits. Relative workdir paths resolve from the repository root; absolute paths are also allowed when the user provides them. Returns stdout, stderr, exit code, and whether output was truncated."
}

func (t *Tool) IsLongRunning() bool {
	return false
}

func (t *Tool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		ParametersJsonSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The bash command to run, for example: ls, find . -name '*.go', rg 'TODO', cat README.md, sed -n '1,80p' main.go.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Optional working directory. Relative paths resolve from the repository root. Absolute paths are allowed when needed.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in seconds. Defaults to 20 and is capped at 120.",
				},
				"max_output_bytes": map[string]any{
					"type":        "integer",
					"description": "Optional per-stream output limit. Defaults to 16384 bytes.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (t *Tool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
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

func (t *Tool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decodeArgs(args)
	if err != nil {
		return nil, err
	}
	result, err := run(t.rootDir, input)
	if err != nil {
		return nil, err
	}
	return toMap(result)
}

func decodeArgs(raw any) (Args, error) {
	if raw == nil {
		return Args{}, fmt.Errorf("args are required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return Args{}, fmt.Errorf("marshal args: %w", err)
	}
	var input Args
	if err := json.Unmarshal(data, &input); err != nil {
		return Args{}, fmt.Errorf("decode args: %w", err)
	}
	return input, nil
}

func toMap(result Result) (map[string]any, error) {
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

func run(rootDir string, input Args) (Result, error) {
	command := strings.TrimSpace(input.Command)
	if command == "" {
		return Result{}, fmt.Errorf("command is required")
	}

	workdir, err := resolveWorkdir(rootDir, input.Workdir)
	if err != nil {
		return Result{}, err
	}

	timeout := input.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultTimeoutSeconds
	}
	if timeout > maxTimeoutSeconds {
		timeout = maxTimeoutSeconds
	}

	maxOutputBytes := input.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = workdir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	result := Result{
		Command:    command,
		Workdir:    workdir,
		ExitCode:   exitCode(runErr),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: duration.Milliseconds(),
	}

	if ctx.Err() == nil && runErr != nil {
		var execErr *exec.Error
		if errors.As(runErr, &execErr) {
			return Result{}, fmt.Errorf("start bash command: %w", runErr)
		}
	}
	if ctx.Err() != nil {
		result.Stderr = joinNonEmpty(result.Stderr, fmt.Sprintf("command timed out after %ds", timeout))
		if result.ExitCode == 0 {
			result.ExitCode = 124
		}
	}

	result.Stdout, result.Truncated = truncate(result.Stdout, maxOutputBytes)
	var stderrTruncated bool
	result.Stderr, stderrTruncated = truncate(result.Stderr, maxOutputBytes)
	result.Truncated = result.Truncated || stderrTruncated

	return result, nil
}

func resolveWorkdir(rootDir, requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return rootDir, nil
	}

	candidate := resolveUserPath(rootDir, requested)

	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("stat workdir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir is not a directory: %s", candidate)
	}
	return candidate, nil
}

func resolveUserPath(rootDir, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return rootDir
	}
	if strings.HasPrefix(requested, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			if requested == "~" {
				return home
			}
			if strings.HasPrefix(requested, "~/") {
				return filepath.Join(home, requested[2:])
			}
		}
	}
	if filepath.IsAbs(requested) {
		return filepath.Clean(requested)
	}
	if looksLikeAbsolutePathWithoutLeadingSlash(requested) {
		return string(os.PathSeparator) + requested
	}
	return filepath.Join(rootDir, requested)
}

func looksLikeAbsolutePathWithoutLeadingSlash(requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.HasPrefix(requested, ".") {
		return false
	}
	first := requested
	if idx := strings.IndexRune(requested, os.PathSeparator); idx >= 0 {
		first = requested[:idx]
	}
	switch first {
	case "Users", "tmp", "var", "etc", "home", "opt", "private", "Volumes":
		return true
	default:
		return false
	}
}

func truncate(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	if maxBytes <= 0 {
		return "", true
	}
	suffix := "\n...[truncated]"
	keep := maxBytes - len(suffix)
	if keep <= 0 {
		return suffix[:maxBytes], true
	}
	return value[:keep] + suffix, true
}

func joinNonEmpty(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
