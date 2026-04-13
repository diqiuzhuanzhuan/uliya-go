package main

import "testing"

func TestConsoleLineEditorBackspaceRemovesASCIIBySingleCell(t *testing.T) {
	editor := newConsoleLineEditor()

	if got, done, interrupted := editor.Apply('a'); got != "a" || done || interrupted {
		t.Fatalf("unexpected first render: got=%q done=%v interrupted=%v", got, done, interrupted)
	}
	if got, done, interrupted := editor.Apply(127); got != "\b \b" || done || interrupted {
		t.Fatalf("unexpected backspace render: got=%q done=%v interrupted=%v", got, done, interrupted)
	}
	if editor.String() != "" {
		t.Fatalf("expected empty buffer, got %q", editor.String())
	}
}

func TestConsoleLineEditorBackspaceRemovesChineseRuneByDisplayWidth(t *testing.T) {
	editor := newConsoleLineEditor()

	editor.Apply('你')
	editor.Apply('好')

	if got, done, interrupted := editor.Apply(127); got != "\b\b  \b\b" || done || interrupted {
		t.Fatalf("unexpected Chinese backspace render: got=%q done=%v interrupted=%v", got, done, interrupted)
	}
	if editor.String() != "你" {
		t.Fatalf("expected remaining buffer to be 你, got %q", editor.String())
	}
}

func TestRuneCellWidthTreatsChineseAsWideAndASCIIAsSingle(t *testing.T) {
	if got := runeCellWidth('a'); got != 1 {
		t.Fatalf("expected ASCII width 1, got %d", got)
	}
	if got := runeCellWidth('你'); got != 2 {
		t.Fatalf("expected Chinese width 2, got %d", got)
	}
}
