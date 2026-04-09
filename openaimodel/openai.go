package openaimodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"strings"
	"time"

	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type Model struct {
	modelName  string
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func New(modelName string, cfg Config) (*Model, error) {
	if strings.TrimSpace(modelName) == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("api key is required")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}

	return &Model{
		modelName:  modelName,
		apiKey:     cfg.APIKey,
		baseURL:    baseURL,
		httpClient: httpClient,
	}, nil
}

func (m *Model) Name() string {
	return m.modelName
}

func (m *Model) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		if !yield(resp, err) {
			return
		}
	}
}

func (m *Model) generate(ctx context.Context, req *adkmodel.LLMRequest) (*adkmodel.LLMResponse, error) {
	chatReq := openAIChatRequest{
		Model:       m.resolveModel(req),
		Messages:    buildMessages(req),
		Temperature: extractTemperature(req),
		Tools:       buildTools(req),
	}
	if len(chatReq.Tools) > 0 {
		chatReq.ToolChoice = "auto"
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create openai request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call openai api: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}

	if httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("openai api returned %s: %s", httpResp.Status, strings.TrimSpace(string(respBody)))
	}

	var chatResp openAIChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("openai response contained no choices")
	}

	content, finishReason, err := buildResponseContent(chatResp.Choices[0].Message, chatResp.Choices[0].FinishReason)
	if err != nil {
		return nil, err
	}

	return &adkmodel.LLMResponse{
		Content:      content,
		ModelVersion: chatResp.Model,
		FinishReason: genai.FinishReason(finishReason),
	}, nil
}

func (m *Model) resolveModel(req *adkmodel.LLMRequest) string {
	if req != nil && strings.TrimSpace(req.Model) != "" {
		return req.Model
	}
	return m.modelName
}

func buildMessages(req *adkmodel.LLMRequest) []openAIMessage {
	if req == nil {
		return nil
	}

	messages := make([]openAIMessage, 0, len(req.Contents))
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		messages = append(messages, contentToMessages(content)...)
	}
	return messages
}

func buildTools(req *adkmodel.LLMRequest) []openAITool {
	if req == nil || req.Config == nil {
		return nil
	}

	var tools []openAITool
	for _, candidate := range req.Config.Tools {
		if candidate == nil {
			continue
		}
		for _, decl := range candidate.FunctionDeclarations {
			if decl == nil || strings.TrimSpace(decl.Name) == "" {
				continue
			}
			tools = append(tools, openAITool{
				Type: "function",
				Function: openAIToolFunction{
					Name:        decl.Name,
					Description: decl.Description,
					Parameters:  decl.ParametersJsonSchema,
				},
			})
		}
	}
	return tools
}

func contentToMessages(content *genai.Content) []openAIMessage {
	role := normalizeRole(strings.TrimSpace(content.Role))
	if role == "" {
		role = "user"
	}

	textParts := make([]string, 0, len(content.Parts))
	toolCalls := make([]openAIToolCall, 0)
	toolResponses := make([]openAIMessage, 0)

	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		if strings.TrimSpace(part.Text) != "" {
			textParts = append(textParts, part.Text)
		}
		if part.FunctionCall != nil {
			toolCalls = append(toolCalls, openAIToolCall{
				ID:   normalizeCallID(part.FunctionCall.ID, part.FunctionCall.Name),
				Type: "function",
				Function: openAIToolCallFunction{
					Name:      part.FunctionCall.Name,
					Arguments: mustJSON(part.FunctionCall.Args),
				},
			})
		}
		if part.FunctionResponse != nil {
			msg := openAIMessage{
				Role:       "tool",
				Name:       part.FunctionResponse.Name,
				ToolCallID: part.FunctionResponse.ID,
			}
			content := mustJSON(part.FunctionResponse.Response)
			msg.Content = &content
			toolResponses = append(toolResponses, msg)
		}
	}

	messages := make([]openAIMessage, 0, 1+len(toolResponses))
	if len(textParts) > 0 || len(toolCalls) > 0 {
		msg := openAIMessage{
			Role:      role,
			ToolCalls: toolCalls,
		}
		if len(textParts) > 0 {
			text := strings.Join(textParts, "\n")
			msg.Content = &text
		}
		messages = append(messages, msg)
	}
	messages = append(messages, toolResponses...)
	return messages
}

func normalizeRole(role string) string {
	switch role {
	case "model":
		return "assistant"
	case "user", "assistant", "system", "tool":
		return role
	default:
		return "user"
	}
}

func buildResponseContent(message openAIMessage, finishReason string) (*genai.Content, string, error) {
	parts := make([]*genai.Part, 0, len(message.ToolCalls)+1)
	if message.Content != nil && strings.TrimSpace(*message.Content) != "" {
		parts = append(parts, &genai.Part{Text: strings.TrimSpace(*message.Content)})
	}

	for _, call := range message.ToolCalls {
		args, err := parseArguments(call.Function.Arguments)
		if err != nil {
			return nil, finishReason, fmt.Errorf("decode tool call args for %s: %w", call.Function.Name, err)
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   call.ID,
				Name: call.Function.Name,
				Args: args,
			},
		})
	}

	if len(parts) == 0 {
		parts = append(parts, &genai.Part{Text: ""})
	}

	return &genai.Content{
		Role:  "model",
		Parts: parts,
	}, finishReason, nil
}

func parseArguments(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func mustJSON(value any) string {
	if value == nil {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func normalizeCallID(id, name string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return "call_" + name + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func extractTemperature(req *adkmodel.LLMRequest) *float64 {
	if req == nil || req.Config == nil || req.Config.Temperature == nil {
		return nil
	}

	value := float64(*req.Config.Temperature)
	return &value
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	Tools       []openAITool    `json:"tools,omitempty"`
	ToolChoice  string          `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
}
