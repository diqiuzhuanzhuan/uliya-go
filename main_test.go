package main

import (
	"testing"
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
