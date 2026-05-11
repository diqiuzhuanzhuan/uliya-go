package main

import (
	"testing"

	"google.golang.org/genai"
)

func TestFallbackIntakeAnalysisUsesPendingIntentReply(t *testing.T) {
	got := fallbackIntakeAnalysis("Use the filename to infer the purpose and categorize them.", "intent")
	if got.Intent == "" || !got.Relevant {
		t.Fatal("expected pending intent reply to be accepted verbatim")
	}
}

func TestFallbackIntakeAnalysisExtractsExplicitPath(t *testing.T) {
	got := fallbackIntakeAnalysis("/Users/ugreen/Downloads", "")
	if got.Path != "/Users/ugreen/Downloads" || !got.Relevant {
		t.Fatalf("unexpected fallback analysis: %#v", got)
	}
}

func TestHasConcreteTaskValuesRequiresBothPathAndIntent(t *testing.T) {
	if hasConcreteTaskValues("/Users/ugreen/Downloads", "") {
		t.Fatal("expected missing intent to be rejected")
	}
	if !hasConcreteTaskValues("/Users/ugreen/Downloads", "Categorize them by filename.") {
		t.Fatal("expected path and intent to be accepted")
	}
}

func TestHasConcreteTaskDefinitionRequiresStateValues(t *testing.T) {
	state := newWorkflowTestState()
	if hasConcreteTaskDefinition(state) {
		t.Fatal("expected empty state to be non-concrete")
	}
	if err := state.Set(stateKeyTargetPath, "/Users/ugreen/Downloads"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if hasConcreteTaskDefinition(state) {
		t.Fatal("expected missing intent in state to be non-concrete")
	}
	if err := state.Set(stateKeyOrganizationIntent, "Categorize by filename"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !hasConcreteTaskDefinition(state) {
		t.Fatal("expected concrete state to be accepted")
	}
}

func TestTokenUsageStatsAddAndFormat(t *testing.T) {
	var run tokenUsageStats
	run.Add(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:     120,
		CandidatesTokenCount: 45,
		TotalTokenCount:      165,
	})

	session := tokenUsageStats{Input: 300, Output: 90, Total: 390}
	session.Merge(run)

	if !run.HasData() {
		t.Fatal("expected run usage to be non-empty")
	}
	if session.Input != 420 || session.Output != 135 || session.Total != 555 {
		t.Fatalf("unexpected merged session usage: %#v", session)
	}

	got := formatTokenUsageSummary(run, session, false)
	want := "[TOKENS] run input=120 output=45 total=165 | session input=420 output=135 total=555"
	if got != want {
		t.Fatalf("unexpected usage summary: got %q want %q", got, want)
	}
}

func TestFormatTokenUsageSummaryChinese(t *testing.T) {
	got := formatTokenUsageSummary(
		tokenUsageStats{Input: 12, Output: 5, Total: 17},
		tokenUsageStats{Input: 30, Output: 9, Total: 39},
		true,
	)
	want := "[TOKENS] 本轮 输入=12 输出=5 总计=17 | 当前会话 输入=30 输出=9 总计=39"
	if got != want {
		t.Fatalf("unexpected Chinese usage summary: got %q want %q", got, want)
	}
}

func TestDetectResponseLanguageKeepsChineseForShortConfirmation(t *testing.T) {
	if got := detectResponseLanguage("zh", "yes"); got != "zh" {
		t.Fatalf("expected zh to be preserved for short confirmation, got %q", got)
	}
	if got := detectResponseLanguage("", "请把下载目录按扩展名整理"); got != "zh" {
		t.Fatalf("expected zh for Chinese input, got %q", got)
	}
	if got := detectResponseLanguage("", "organize files by extension"); got != "en" {
		t.Fatalf("expected en for English input, got %q", got)
	}
}
