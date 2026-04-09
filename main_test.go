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
