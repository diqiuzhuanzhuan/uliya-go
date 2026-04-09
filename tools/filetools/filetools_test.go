package filetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListFilesToolListsTopLevelFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	tool := &ListFilesTool{rootDir: root}
	got, err := tool.Run(nil, map[string]any{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	files := got["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("expected 2 entries, got %#v", files)
	}
}

func TestFindFilesToolFindsMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("docs"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &FindFilesTool{rootDir: root}
	got, err := tool.Run(nil, map[string]any{"query": "main"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	files := got["files"].([]any)
	if len(files) != 1 || files[0].(string) != "main.go" {
		t.Fatalf("unexpected matches: %#v", files)
	}
}

func TestGlobFilesToolFindsPatternMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "x.go"), []byte("package sub"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &GlobFilesTool{rootDir: root}
	got, err := tool.Run(nil, map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	files := got["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("unexpected matches: %#v", files)
	}
}

func TestGrepTextToolSupportsContentMode(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nworld\nhello again"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &GrepTextTool{rootDir: root}
	got, err := tool.Run(nil, map[string]any{
		"pattern":     "hello",
		"output_mode": "content",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	matches := got["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("unexpected matches: %#v", matches)
	}
}

func TestReadAndWriteFileTools(t *testing.T) {
	root := t.TempDir()
	writeTool := &WriteFileTool{rootDir: root}
	readTool := &ReadFileTool{rootDir: root}

	if _, err := writeTool.Run(nil, map[string]any{
		"path":        "notes/todo.txt",
		"content":     "hello",
		"create_dirs": true,
		"overwrite":   true,
	}); err != nil {
		t.Fatalf("write Run() error = %v", err)
	}

	got, err := readTool.Run(nil, map[string]any{"path": "notes/todo.txt"})
	if err != nil {
		t.Fatalf("read Run() error = %v", err)
	}
	if got["content"].(string) != "     1\thello" {
		t.Fatalf("unexpected content: %#v", got)
	}
}

func TestReadFileToolSupportsOffsetAndLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	content := "a\nb\nc\nd"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &ReadFileTool{rootDir: root}
	got, err := tool.Run(nil, map[string]any{
		"path":   "sample.txt",
		"offset": 1,
		"limit":  2,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got["content"].(string) != "     2\tb\n     3\tc" {
		t.Fatalf("unexpected content: %#v", got["content"])
	}
}

func TestEditFileToolReplacesUniqueSnippet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	readTool := &ReadFileTool{rootDir: root}
	if _, err := readTool.Run(nil, map[string]any{"path": "main.go"}); err != nil {
		t.Fatalf("read Run() error = %v", err)
	}

	tool := &EditFileTool{rootDir: root}
	_, err := tool.Run(nil, map[string]any{
		"path":       "main.go",
		"old_string": "world",
		"new_string": "gopher",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello gopher" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestEditFileRequiresReadFirst(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &EditFileTool{rootDir: root}
	_, err := tool.Run(nil, map[string]any{
		"path":       "main.go",
		"old_string": "world",
		"new_string": "gopher",
	})
	if err == nil {
		t.Fatal("expected read-before-edit error")
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteFileToolRejectsExistingFileWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &WriteFileTool{rootDir: root}
	_, err := tool.Run(nil, map[string]any{
		"path":    "a.txt",
		"content": "new",
	})
	if err == nil {
		t.Fatal("expected overwrite protection error")
	}
	if !strings.Contains(err.Error(), "file already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadFileToolAllowsAbsolutePathOutsideRepo(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	path := filepath.Join(other, "note.txt")
	if err := os.WriteFile(path, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tool := &ReadFileTool{rootDir: root}
	got, err := tool.Run(nil, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got["path"].(string) != path {
		t.Fatalf("expected absolute path %q, got %#v", path, got["path"])
	}
}

func TestWriteFileToolAllowsAbsolutePathOutsideRepo(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	path := filepath.Join(other, "created.txt")

	tool := &WriteFileTool{rootDir: root}
	_, err := tool.Run(nil, map[string]any{
		"path":      path,
		"content":   "hello",
		"overwrite": true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected file content: %q", string(data))
	}
}

func TestResolveUserPathTreatsUsersPrefixAsAbsoluteLike(t *testing.T) {
	root := t.TempDir()
	got := resolveUserPath(root, "Users/example/docs")
	if got != "/Users/example/docs" {
		t.Fatalf("expected absolute-like path, got %q", got)
	}
}
