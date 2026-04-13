package openaimodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	adkagent "google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/genai"
)

const defaultBaseURL = "https://api.openai.com/v1"

const (
	maxRequestAttempts = 3
	initialRetryDelay  = 500 * time.Millisecond
)

type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type logLabelContextKey struct{}

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

func WithLogLabel(ctx context.Context, label string) context.Context {
	if ctx == nil || strings.TrimSpace(label) == "" {
		return ctx
	}
	return context.WithValue(ctx, logLabelContextKey{}, strings.TrimSpace(label))
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
	logModelRequest(ctx, chatReq)
	if len(chatReq.Messages) == 0 {
		return nil, fmt.Errorf("openai request has no messages")
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	respBody, err := m.doChatCompletionRequest(ctx, body)
	if err != nil {
		return nil, err
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

func logModelRequest(ctx context.Context, req openAIChatRequest) {
	prefix := buildLogPrefix(ctx)
	if len(req.Messages) == 0 {
		log.Printf("%s model=%s messages=0 tools=%d", prefix, req.Model, len(req.Tools))
		return
	}

	log.Printf("%s model=%s messages=%d tools=%d", prefix, req.Model, len(req.Messages), len(req.Tools))
	for i, message := range req.Messages {
		if i >= 4 {
			log.Printf("%s ... %d more message(s) omitted", prefix, len(req.Messages)-i)
			break
		}

		switch {
		case message.Content != nil && strings.TrimSpace(*message.Content) != "":
			log.Printf("%s %s: %s", prefix, message.Role, truncatePreview(*message.Content, 400))
		case len(message.ToolCalls) > 0:
			log.Printf("%s %s tool_calls=%s", prefix, message.Role, summarizeToolCalls(message.ToolCalls))
		default:
			log.Printf("%s %s (empty content)", prefix, message.Role)
		}
	}
}

func (m *Model) doChatCompletionRequest(ctx context.Context, body []byte) ([]byte, error) {
	var lastErr error
	delay := initialRetryDelay

	for attempt := 1; attempt <= maxRequestAttempts; attempt++ {
		respBody, err := m.doChatCompletionRequestOnce(ctx, body)
		if err == nil {
			return respBody, nil
		}

		lastErr = err
		if !shouldRetryRequest(ctx, err) || attempt == maxRequestAttempts {
			break
		}

		log.Printf("%s retrying openai request after attempt %d/%d: %v", buildLogPrefix(ctx), attempt, maxRequestAttempts, err)
		if sleepErr := sleepWithContext(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
		delay *= 2
	}

	return nil, lastErr
}

func (m *Model) doChatCompletionRequestOnce(ctx context.Context, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create openai request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return nil, &requestError{
			Err:       fmt.Errorf("call openai api: %w", err),
			Retryable: true,
		}
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &requestError{
			Err:       fmt.Errorf("read openai response: %w", err),
			Retryable: true,
		}
	}

	if httpResp.StatusCode >= 300 {
		return nil, &requestError{
			Err:        fmt.Errorf("openai api returned %s: %s", httpResp.Status, strings.TrimSpace(string(respBody))),
			Retryable:  httpResp.StatusCode == http.StatusTooManyRequests || httpResp.StatusCode >= 500,
			StatusCode: httpResp.StatusCode,
		}
	}

	return respBody, nil
}

type requestError struct {
	Err        error
	Retryable  bool
	StatusCode int
}

func (e *requestError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *requestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func shouldRetryRequest(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		return reqErr.Retryable
	}
	return false
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func buildLogPrefix(ctx context.Context) string {
	parts := []string{"[MODEL REQUEST]"}
	if label := logLabelFromContext(ctx); label != "" {
		parts = append(parts, "[label="+label+"]")
	}
	if agentName := agentNameFromContext(ctx); agentName != "" {
		parts = append(parts, "[agent="+agentName+"]")
	}
	return strings.Join(parts, "")
}

func logLabelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	label, _ := ctx.Value(logLabelContextKey{}).(string)
	return strings.TrimSpace(label)
}

func agentNameFromContext(ctx context.Context) string {
	type agentNamer interface {
		AgentName() string
	}
	type agentWithMethod interface {
		Agent() adkagent.Agent
	}

	if ctx == nil {
		return ""
	}
	if named, ok := ctx.(agentNamer); ok {
		return strings.TrimSpace(named.AgentName())
	}
	if withAgent, ok := ctx.(agentWithMethod); ok && withAgent.Agent() != nil {
		return strings.TrimSpace(withAgent.Agent().Name())
	}
	return ""
}

func summarizeToolCalls(calls []openAIToolCall) string {
	parts := make([]string, 0, len(calls))
	for i, call := range calls {
		if i >= 3 {
			parts = append(parts, "...")
			break
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", call.Function.Name, truncatePreview(call.Function.Arguments, 120)))
	}
	return strings.Join(parts, ", ")
}

func truncatePreview(text string, max int) string {
	text = strings.TrimSpace(text)
	if text == "" || max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "...(truncated)"
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

	messages := make([]openAIMessage, 0, len(req.Contents)+1)
	if req.Config != nil && req.Config.SystemInstruction != nil {
		messages = append(messages, systemInstructionToMessages(req.Config.SystemInstruction)...)
	}
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		messages = append(messages, contentToMessages(content)...)
	}
	return messages
}

func systemInstructionToMessages(content *genai.Content) []openAIMessage {
	if content == nil {
		return nil
	}
	systemContent := &genai.Content{
		Role:  "system",
		Parts: content.Parts,
	}
	return contentToMessages(systemContent)
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
