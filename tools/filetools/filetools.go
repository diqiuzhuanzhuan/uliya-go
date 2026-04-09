package filetools

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const defaultReadMaxBytes = 16 * 1024
const defaultReadLimit = 100

type ListFilesTool struct {
	rootDir string
}

type FindFilesTool struct {
	rootDir string
}

type GlobFilesTool struct {
	rootDir string
}

type GrepTextTool struct {
	rootDir string
}

type ReadFileTool struct {
	rootDir string
}

type WriteFileTool struct {
	rootDir string
}

type EditFileTool struct {
	rootDir string
}

type listFilesArgs struct {
	Path      string `json:"path,omitempty"`
	Recursive *bool  `json:"recursive,omitempty"`
}

type listFilesResult struct {
	Path  string   `json:"path"`
	Files []string `json:"files"`
}

type findFilesArgs struct {
	Query         string `json:"query"`
	Path          string `json:"path,omitempty"`
	CaseSensitive bool   `json:"case_sensitive,omitempty"`
}

type findFilesResult struct {
	Query string   `json:"query"`
	Path  string   `json:"path"`
	Files []string `json:"files"`
}

type globFilesArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

type globFilesResult struct {
	Pattern string   `json:"pattern"`
	Path    string   `json:"path"`
	Files   []string `json:"files"`
}

type grepTextArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
}

type grepTextMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Content string `json:"content,omitempty"`
	Count   int    `json:"count,omitempty"`
}

type grepTextResult struct {
	Pattern string          `json:"pattern"`
	Path    string          `json:"path"`
	Matches []grepTextMatch `json:"matches"`
}

type readFileArgs struct {
	Path     string `json:"path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

type readFileResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	SizeBytes int    `json:"size_bytes"`
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

type editFileResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

type writeFileArgs struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	CreateDirs  bool   `json:"create_dirs,omitempty"`
	Append      bool   `json:"append,omitempty"`
	Overwrite   bool   `json:"overwrite,omitempty"`
	ExpectedOld string `json:"expected_old_content,omitempty"`
}

type writeFileResult struct {
	Path         string `json:"path"`
	Appended     bool   `json:"appended"`
	BytesWritten int    `json:"bytes_written"`
}

type readRecord struct {
	modTime int64
	size    int64
}

var (
	readTrackerMu sync.Mutex
	readTracker   = map[string]readRecord{}
)

func New(rootDir string) ([]tool.Tool, error) {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve root dir: %w", err)
	}
	return []tool.Tool{
		&ListFilesTool{rootDir: rootDir},
		&FindFilesTool{rootDir: rootDir},
		&GlobFilesTool{rootDir: rootDir},
		&GrepTextTool{rootDir: rootDir},
		&ReadFileTool{rootDir: rootDir},
		&EditFileTool{rootDir: rootDir},
		&WriteFileTool{rootDir: rootDir},
	}, nil
}

func (t *ListFilesTool) Name() string { return "list_files" }
func (t *ListFilesTool) Description() string {
	return "List files and directories. By default this is recursive so the agent can see the directory tree immediately. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Use this instead of ls for ordinary file browsing. When the user provides a directory and asks what is inside or asks you to organize it, call this tool first."
}
func (t *ListFilesTool) IsLongRunning() bool { return false }
func (t *ListFilesTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Optional directory path. Relative paths resolve from the repository root. Defaults to repository root.",
			},
			"recursive": map[string]any{
				"type":        "boolean",
				"description": "Whether to list files recursively. Defaults to true.",
			},
		},
		"additionalProperties": false,
	})
}
func (t *ListFilesTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *ListFilesTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[listFilesArgs](args)
	if err != nil {
		return nil, err
	}
	dir, rel, err := resolveDir(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	recursive := true
	if input.Recursive != nil {
		recursive = *input.Recursive
	}
	files, err := listFiles(dir, rel, recursive)
	if err != nil {
		return nil, err
	}
	return toMap(listFilesResult{Path: rel, Files: files})
}

func (t *FindFilesTool) Name() string { return "find_files" }
func (t *FindFilesTool) Description() string {
	return "Find files or directories by substring match. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Use this instead of bash find for normal searches."
}
func (t *FindFilesTool) IsLongRunning() bool { return false }
func (t *FindFilesTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Substring to search for in file or directory names.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional directory path. Relative paths resolve from the repository root. Defaults to repository root.",
			},
			"case_sensitive": map[string]any{
				"type":        "boolean",
				"description": "Whether name matching should be case-sensitive.",
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	})
}
func (t *FindFilesTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *FindFilesTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[findFilesArgs](args)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	dir, rel, err := resolveDir(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	files, err := findFiles(dir, rel, query, input.CaseSensitive)
	if err != nil {
		return nil, err
	}
	return toMap(findFilesResult{Query: query, Path: rel, Files: files})
}

func (t *GlobFilesTool) Name() string { return "glob_files" }
func (t *GlobFilesTool) Description() string {
	return "Find files by glob pattern. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Prefer this over bash find for normal pattern-based searches."
}
func (t *GlobFilesTool) IsLongRunning() bool { return false }
func (t *GlobFilesTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern such as *.go, **/*.md, or cmd/*.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional directory path. Relative paths resolve from the repository root. Defaults to repository root.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	})
}
func (t *GlobFilesTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *GlobFilesTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[globFilesArgs](args)
	if err != nil {
		return nil, err
	}
	pattern := strings.TrimSpace(input.Pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	dir, rel, err := resolveDir(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	files, err := globFiles(dir, pattern)
	if err != nil {
		return nil, err
	}
	return toMap(globFilesResult{Pattern: pattern, Path: rel, Files: files})
}

func (t *GrepTextTool) Name() string { return "grep_text" }
func (t *GrepTextTool) Description() string {
	return "Search for literal text inside files. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Prefer this over bash grep for ordinary content searches."
}
func (t *GrepTextTool) IsLongRunning() bool { return false }
func (t *GrepTextTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Literal text to search for.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional directory or file path. Relative paths resolve from the repository root. Defaults to repository root.",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "Optional glob filter such as *.go or **/*.md.",
			},
			"output_mode": map[string]any{
				"type":        "string",
				"description": "One of files_with_matches, content, or count. Defaults to files_with_matches.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	})
}
func (t *GrepTextTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *GrepTextTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[grepTextArgs](args)
	if err != nil {
		return nil, err
	}
	pattern := strings.TrimSpace(input.Pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	path, rel, err := resolvePath(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	matches, err := grepText(path, pattern, strings.TrimSpace(input.Glob), normalizeOutputMode(input.OutputMode))
	if err != nil {
		return nil, err
	}
	return toMap(grepTextResult{Pattern: pattern, Path: rel, Matches: matches})
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read a file with optional pagination. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Returns line-numbered text to make follow-up edits easier. If the user already gave you a path, prefer calling this tool instead of asking them to paste file contents."
}
func (t *ReadFileTool) IsLongRunning() bool { return false }
func (t *ReadFileTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path. Relative paths resolve from the repository root. Absolute paths are also allowed when needed.",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Optional 0-based starting line offset. Use for pagination in large files.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Optional maximum number of lines to read. Defaults to 100.",
			},
			"max_bytes": map[string]any{
				"type":        "integer",
				"description": "Optional maximum bytes to return after line formatting. Defaults to 16384.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	})
}
func (t *ReadFileTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *ReadFileTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[readFileArgs](args)
	if err != nil {
		return nil, err
	}
	path, rel, err := resolveFilePath(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	maxBytes := input.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadMaxBytes
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	content := formatReadContent(string(data), input.Offset, input.Limit)
	truncated := false
	if len(content) > maxBytes {
		content = content[:maxBytes] + "\n...[truncated]"
		truncated = true
	}
	markFileRead(path)
	return toMap(readFileResult{
		Path:      rel,
		Content:   content,
		Truncated: truncated,
		SizeBytes: len(data),
	})
}

func (t *EditFileTool) Name() string { return "edit_file" }
func (t *EditFileTool) Description() string {
	return "Edit an existing file using exact string replacement. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Prefer this over rewriting the whole file when making a targeted change."
}
func (t *EditFileTool) IsLongRunning() bool { return false }
func (t *EditFileTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path. Relative paths resolve from the repository root. Absolute paths are also allowed when needed.",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Exact text to replace.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Replacement text.",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "Whether to replace all occurrences. If false, old_string must be unique.",
			},
		},
		"required":             []string{"path", "old_string", "new_string"},
		"additionalProperties": false,
	})
}
func (t *EditFileTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *EditFileTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[editFileArgs](args)
	if err != nil {
		return nil, err
	}
	if input.OldString == input.NewString {
		return nil, fmt.Errorf("old_string and new_string must be different")
	}
	path, rel, err := resolveFilePath(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	if err := ensureFileWasRead(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	matches := strings.Count(content, input.OldString)
	if matches == 0 {
		return nil, fmt.Errorf("old_string not found in file")
	}
	if !input.ReplaceAll && matches != 1 {
		return nil, fmt.Errorf("old_string matched %d times; set replace_all=true or choose a unique snippet", matches)
	}
	replacements := 1
	if input.ReplaceAll {
		replacements = matches
		content = strings.ReplaceAll(content, input.OldString, input.NewString)
	} else {
		content = strings.Replace(content, input.OldString, input.NewString, 1)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	clearFileRead(path)
	return toMap(editFileResult{Path: rel, Replacements: replacements})
}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Write or append file content. Relative paths resolve from the repository root; absolute paths are also allowed when the user provides them. Use this for normal file creation and edits instead of shell redirection."
}
func (t *WriteFileTool) IsLongRunning() bool { return false }
func (t *WriteFileTool) Declaration() *genai.FunctionDeclaration {
	return declaration(t.Name(), t.Description(), map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path. Relative paths resolve from the repository root. Absolute paths are also allowed when needed.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write.",
			},
			"create_dirs": map[string]any{
				"type":        "boolean",
				"description": "Whether to create parent directories automatically.",
			},
			"append": map[string]any{
				"type":        "boolean",
				"description": "Whether to append instead of replacing file content.",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "Whether to allow overwriting an existing file when append is false.",
			},
			"expected_old_content": map[string]any{
				"type":        "string",
				"description": "Optional exact current file content to match before writing, for safer edits.",
			},
		},
		"required":             []string{"path", "content"},
		"additionalProperties": false,
	})
}
func (t *WriteFileTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return appendTool(t, req)
}
func (t *WriteFileTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	input, err := decode[writeFileArgs](args)
	if err != nil {
		return nil, err
	}
	path, rel, err := resolvePath(t.rootDir, input.Path)
	if err != nil {
		return nil, err
	}
	if input.CreateDirs {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create parent dirs: %w", err)
		}
	}
	if !input.Append {
		if _, err := os.Stat(path); err == nil && !input.Overwrite && input.ExpectedOld == "" {
			return nil, fmt.Errorf("file already exists: %s", rel)
		}
	}

	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read existing file: %w", err)
	}
	if input.ExpectedOld != "" && string(existing) != input.ExpectedOld {
		return nil, fmt.Errorf("existing file content does not match expected_old_content")
	}

	flags := os.O_CREATE | os.O_WRONLY
	if input.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open file for writing: %w", err)
	}
	defer f.Close()

	n, err := f.WriteString(input.Content)
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	clearFileRead(path)
	return toMap(writeFileResult{
		Path:         rel,
		Appended:     input.Append,
		BytesWritten: n,
	})
}

func declaration(name, description string, schema map[string]any) *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:                 name,
		Description:          description,
		ParametersJsonSchema: schema,
	}
}

func appendTool(t interface {
	Name() string
	Declaration() *genai.FunctionDeclaration
}, req *model.LLMRequest) error {
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

func decode[T any](raw any) (T, error) {
	var out T
	if raw == nil {
		return out, fmt.Errorf("args are required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return out, fmt.Errorf("marshal args: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("decode args: %w", err)
	}
	return out, nil
}

func toMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func resolveDir(rootDir, requested string) (string, string, error) {
	path, rel, err := resolvePath(rootDir, requested)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("path is not a directory: %s", rel)
	}
	return path, rel, nil
}

func resolveFilePath(rootDir, requested string) (string, string, error) {
	path, rel, err := resolvePath(rootDir, requested)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("stat path: %w", err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("path is a directory, not a file: %s", rel)
	}
	return path, rel, nil
}

func resolvePath(rootDir, requested string) (string, string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = "."
	}
	candidate := resolveUserPath(rootDir, requested)
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(rootDir, candidate)
	if err != nil {
		return "", "", fmt.Errorf("make relative path: %w", err)
	}
	if filepath.IsAbs(requested) {
		return candidate, candidate, nil
	}
	if rel == "." {
		return candidate, ".", nil
	}
	return candidate, filepath.ToSlash(rel), nil
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

func listFiles(dir, rel string, recursive bool) ([]string, error) {
	var files []string
	if recursive {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == dir {
				return nil
			}
			itemRel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			item := filepath.ToSlash(itemRel)
			if d.IsDir() {
				item += "/"
			}
			files = append(files, item)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk directory: %w", err)
		}
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read directory: %w", err)
		}
		for _, entry := range entries {
			item := entry.Name()
			if entry.IsDir() {
				item += "/"
			}
			files = append(files, item)
		}
	}
	sort.Strings(files)
	return files, nil
}

func findFiles(dir, rel, query string, caseSensitive bool) ([]string, error) {
	var files []string
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(needle)
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		name := d.Name()
		matchTarget := name
		if !caseSensitive {
			matchTarget = strings.ToLower(name)
		}
		if !strings.Contains(matchTarget, needle) {
			return nil
		}
		itemRel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		item := filepath.ToSlash(itemRel)
		if d.IsDir() {
			item += "/"
		}
		files = append(files, item)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func globFiles(dir, pattern string) ([]string, error) {
	matcher, err := compileGlob(pattern)
	if err != nil {
		return nil, err
	}
	var files []string
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if d.IsDir() {
			name += "/"
		}
		if matcher.MatchString(strings.TrimSuffix(name, "/")) {
			files = append(files, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func grepText(path, pattern, globPattern, outputMode string) ([]grepTextMatch, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}
	var globMatcher *regexp.Regexp
	if globPattern != "" {
		globMatcher, err = compileGlob(globPattern)
		if err != nil {
			return nil, err
		}
	}
	matches := make([]grepTextMatch, 0)
	walkFile := func(root, filePath string) error {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil
		}
		content := string(data)
		if strings.IndexByte(content, 0) >= 0 {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if globMatcher != nil && !globMatcher.MatchString(rel) {
			return nil
		}
		lines := strings.Split(content, "\n")
		switch outputMode {
		case "content":
			for i, line := range lines {
				if strings.Contains(line, pattern) {
					matches = append(matches, grepTextMatch{Path: rel, Line: i + 1, Content: line})
				}
			}
		case "count":
			count := 0
			for _, line := range lines {
				count += strings.Count(line, pattern)
			}
			if count > 0 {
				matches = append(matches, grepTextMatch{Path: rel, Count: count})
			}
		default:
			for _, line := range lines {
				if strings.Contains(line, pattern) {
					matches = append(matches, grepTextMatch{Path: rel})
					break
				}
			}
		}
		return nil
	}
	if info.IsDir() {
		err = filepath.WalkDir(path, func(filePath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			return walkFile(path, filePath)
		})
	} else {
		err = walkFile(filepath.Dir(path), path)
	}
	if err != nil {
		return nil, fmt.Errorf("search files: %w", err)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path == matches[j].Path {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Path < matches[j].Path
	})
	return matches, nil
}

func formatReadContent(content string, offset, limit int) string {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultReadLimit
	}
	lines := strings.Split(content, "\n")
	if offset >= len(lines) {
		return ""
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	selected := lines[offset:end]
	var b strings.Builder
	for i, line := range selected {
		fmt.Fprintf(&b, "%6d\t%s", offset+i+1, line)
		if i < len(selected)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 3
				} else {
					b.WriteString(".*")
					i += 2
				}
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
	}
	return re, nil
}

func normalizeOutputMode(mode string) string {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "", "files_with_matches":
		return "files_with_matches"
	case "content", "count":
		return mode
	default:
		return "files_with_matches"
	}
}

func markFileRead(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	readTrackerMu.Lock()
	defer readTrackerMu.Unlock()
	readTracker[path] = readRecord{modTime: info.ModTime().UnixNano(), size: info.Size()}
}

func clearFileRead(path string) {
	readTrackerMu.Lock()
	defer readTrackerMu.Unlock()
	delete(readTracker, path)
}

func ensureFileWasRead(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat file before edit: %w", err)
	}
	readTrackerMu.Lock()
	record, ok := readTracker[path]
	readTrackerMu.Unlock()
	if !ok {
		return fmt.Errorf("file must be read with read_file before editing")
	}
	if record.modTime != info.ModTime().UnixNano() || record.size != info.Size() {
		return fmt.Errorf("file changed since last read; read it again before editing")
	}
	return nil
}
