package openaimodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
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

	text := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	return &adkmodel.LLMResponse{
		Content: &genai.Content{
			Role: "model",
			Parts: []*genai.Part{
				{Text: text},
			},
		},
		ModelVersion: chatResp.Model,
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

		role := strings.TrimSpace(content.Role)
		if role == "" {
			role = "user"
		}

		text := contentText(content)
		if strings.TrimSpace(text) == "" {
			continue
		}

		messages = append(messages, openAIMessage{
			Role:    normalizeRole(role),
			Content: text,
		})
	}
	return messages
}

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}

	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
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

func extractTemperature(req *adkmodel.LLMRequest) *float64 {
	if req == nil || req.Config == nil || req.Config.Temperature == nil {
		return nil
	}

	value := float64(*req.Config.Temperature)
	return &value
}

type openAIChatRequest struct {
	Model       string           `json:"model"`
	Messages    []openAIMessage  `json:"messages"`
	Temperature *float64         `json:"temperature,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
}
