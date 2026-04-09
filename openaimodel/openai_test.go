package openaimodel

import (
	"testing"

	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBuildMessagesIncludesToolCallsAndResponses(t *testing.T) {
	req := &adkmodel.LLMRequest{
		Contents: []*genai.Content{
			{
				Role: "user",
				Parts: []*genai.Part{
					{Text: "list files"},
				},
			},
			{
				Role: "model",
				Parts: []*genai.Part{
					{
						FunctionCall: &genai.FunctionCall{
							ID:   "call_1",
							Name: "bash",
							Args: map[string]any{"command": "ls"},
						},
					},
				},
			},
			{
				Role: "user",
				Parts: []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							ID:       "call_1",
							Name:     "bash",
							Response: map[string]any{"stdout": "main.go"},
						},
					},
				},
			},
		},
	}

	got := buildMessages(req)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content == nil || *got[0].Content != "list files" {
		t.Fatalf("unexpected user message: %#v", got[0])
	}
	if got[1].Role != "assistant" || len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].Function.Name != "bash" {
		t.Fatalf("unexpected assistant tool call message: %#v", got[1])
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "call_1" {
		t.Fatalf("unexpected tool response message: %#v", got[2])
	}
}

func TestBuildToolsFromRequest(t *testing.T) {
	req := &adkmodel.LLMRequest{
		Config: &genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "bash",
							Description: "run bash",
							ParametersJsonSchema: map[string]any{
								"type": "object",
							},
						},
					},
				},
			},
		},
	}

	got := buildTools(req)
	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if got[0].Function.Name != "bash" {
		t.Fatalf("expected bash tool, got %#v", got[0])
	}
}

func TestBuildResponseContentParsesToolCalls(t *testing.T) {
	content, _, err := buildResponseContent(openAIMessage{
		Content: strPtr("running command"),
		ToolCalls: []openAIToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: openAIToolCallFunction{
					Name:      "bash",
					Arguments: `{"command":"pwd"}`,
				},
			},
		},
	}, "tool_calls")
	if err != nil {
		t.Fatalf("buildResponseContent() error = %v", err)
	}
	if len(content.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(content.Parts))
	}
	if content.Parts[0].Text != "running command" {
		t.Fatalf("unexpected text part: %#v", content.Parts[0])
	}
	if content.Parts[1].FunctionCall == nil || content.Parts[1].FunctionCall.Name != "bash" {
		t.Fatalf("unexpected function call part: %#v", content.Parts[1])
	}
}

func strPtr(value string) *string {
	return &value
}
