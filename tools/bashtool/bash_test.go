package bashtool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExecutesCommandInRepo(t *testing.T) {
	root := t.TempDir()
	result, err := run(root, Args{
		Command: "printf 'hello'",
	})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d, stderr=%q", result.ExitCode, result.Stderr)
	}
	if result.Stdout != "hello" {
		t.Fatalf("expected stdout hello, got %q", result.Stdout)
	}
	if result.Workdir != root {
		t.Fatalf("expected workdir %q, got %q", root, result.Workdir)
	}
}

func TestRunRejectsMissingWorkdir(t *testing.T) {
	root := t.TempDir()
	_, err := run(root, Args{
		Command: "pwd",
		Workdir: "../outside",
	})
	if err == nil {
		t.Fatal("expected error for missing workdir")
	}
	if !strings.Contains(err.Error(), "stat workdir") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveWorkdirAllowsNestedPath(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	got, err := resolveWorkdir(root, "sub")
	if err != nil {
		t.Fatalf("resolveWorkdir() error = %v", err)
	}
	if got != subdir {
		t.Fatalf("expected %q, got %q", subdir, got)
	}
}

func TestResolveWorkdirAllowsAbsolutePathOutsideRepo(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()

	got, err := resolveWorkdir(root, other)
	if err != nil {
		t.Fatalf("resolveWorkdir() error = %v", err)
	}
	if got != other {
		t.Fatalf("expected %q, got %q", other, got)
	}
}

func TestResolveUserPathTreatsUsersPrefixAsAbsoluteLike(t *testing.T) {
	root := t.TempDir()
	got := resolveUserPath(root, "Users/example/docs")
	if got != "/Users/example/docs" {
		t.Fatalf("expected absolute-like path, got %q", got)
	}
}
