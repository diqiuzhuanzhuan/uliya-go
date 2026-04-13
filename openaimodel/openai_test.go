package openaimodel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestBuildMessagesIncludesSystemInstruction(t *testing.T) {
	req := &adkmodel.LLMRequest{
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("follow the plan", genai.RoleUser),
		},
		Contents: []*genai.Content{
			genai.NewContentFromText("organize files", genai.RoleUser),
		},
	}

	got := buildMessages(req)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != "system" || got[0].Content == nil || *got[0].Content != "follow the plan" {
		t.Fatalf("unexpected system message: %#v", got[0])
	}
	if got[1].Role != "user" || got[1].Content == nil || *got[1].Content != "organize files" {
		t.Fatalf("unexpected user message: %#v", got[1])
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

func TestGenerateContentRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if call < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	model, err := New("test-model", Config{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &adkmodel.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("hello", genai.RoleUser),
		},
	}

	var got *adkmodel.LLMResponse
	for resp, runErr := range model.GenerateContent(context.Background(), req, false) {
		if runErr != nil {
			t.Fatalf("GenerateContent() error = %v", runErr)
		}
		got = resp
	}

	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
	if got == nil || got.Content == nil || len(got.Content.Parts) == 0 || got.Content.Parts[0].Text != "ok" {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestGenerateContentDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()

	model, err := New("test-model", Config{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := &adkmodel.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("hello", genai.RoleUser),
		},
	}

	for _, runErr := range model.GenerateContent(context.Background(), req, false) {
		if runErr == nil {
			t.Fatal("expected client error")
		}
		if calls.Load() != 1 {
			t.Fatalf("expected 1 attempt, got %d", calls.Load())
		}
		return
	}

	t.Fatal("expected GenerateContent() to yield an error")
}
