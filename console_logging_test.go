package main

import (
	"strings"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestContentConsoleOutputIncludesAgentAndToolLabels(t *testing.T) {
	event := session.NewEvent("inv-1")
	event.Author = "executor_agent"
	event.LLMResponse = model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "move_file",
						Args: map[string]any{"src": "a", "dst": "b"},
					},
				},
				{
					FunctionResponse: &genai.FunctionResponse{
						Name:     "move_file",
						Response: map[string]any{"moved": true},
					},
				},
			},
		},
	}

	got := contentConsoleOutput(event)
	if !strings.Contains(got, "[TOOL CALL][agent=executor_agent][tool=move_file]") {
		t.Fatalf("expected tool call label in output, got %q", got)
	}
	if !strings.Contains(got, "[TOOL RESPONSE][agent=executor_agent][tool=move_file]") {
		t.Fatalf("expected tool response label in output, got %q", got)
	}
}
