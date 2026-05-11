package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"google.golang.org/adk/agent"
	adkmodel "google.golang.org/adk/model"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func newRuntimeLoggingPlugin() (*plugin.Plugin, error) {
	logger := &runtimeLogger{name: "runtime_log"}
	return plugin.New(plugin.Config{
		Name:                  logger.name,
		OnUserMessageCallback: logger.onUserMessage,
		BeforeRunCallback:     logger.beforeRun,
		OnEventCallback:       logger.onEvent,
		AfterRunCallback:      logger.afterRun,
		BeforeModelCallback:   logger.beforeModel,
		AfterModelCallback:    logger.afterModel,
		OnModelErrorCallback:  logger.onModelError,
		BeforeToolCallback:    logger.beforeTool,
		AfterToolCallback:     logger.afterTool,
		OnToolErrorCallback:   logger.onToolError,
	})
}

func newPromptPreviewPlugin() (*plugin.Plugin, error) {
	logger := &runtimeLogger{name: "prompt_preview"}
	return plugin.New(plugin.Config{
		Name:                logger.name,
		BeforeModelCallback: logger.previewModelInput,
	})
}

type runtimeLogger struct {
	name string
}

func (l *runtimeLogger) logf(format string, args ...any) {
	log.Printf("[%s] %s", l.name, fmt.Sprintf(format, args...))
}

func (l *runtimeLogger) onUserMessage(ctx agent.InvocationContext, userMessage *genai.Content) (*genai.Content, error) {
	l.logf("[User] session=%s agent=%s content=%s", ctx.Session().ID(), agentNameFromInvocation(ctx), summarizeContentForRuntimeLog(userMessage, 160))
	return nil, nil
}

func (l *runtimeLogger) beforeRun(ctx agent.InvocationContext) (*genai.Content, error) {
	l.logf("[Run Start] invocation=%s session=%s agent=%s", ctx.InvocationID(), ctx.Session().ID(), agentNameFromInvocation(ctx))
	return nil, nil
}

func (l *runtimeLogger) onEvent(_ agent.InvocationContext, event *session.Event) (*session.Event, error) {
	if event == nil {
		return nil, nil
	}
	if usage := formatUsageMetadataForRuntimeLog(event.LLMResponse.UsageMetadata); usage != "" {
		l.logf("[Tokens] source=event author=%s %s", event.Author, usage)
	}
	if event.IsFinalResponse() {
		l.logf("[Final] author=%s content=%s", event.Author, summarizeContentForRuntimeLog(event.Content, 200))
	}
	return nil, nil
}

func (l *runtimeLogger) afterRun(ctx agent.InvocationContext) {
	l.logf("[Run Done] invocation=%s session=%s agent=%s", ctx.InvocationID(), ctx.Session().ID(), agentNameFromInvocation(ctx))
}

func (l *runtimeLogger) beforeModel(ctx agent.CallbackContext, req *adkmodel.LLMRequest) (*adkmodel.LLMResponse, error) {
	modelName := ""
	if req != nil {
		modelName = req.Model
	}
	l.logf("[Model] start agent=%s model=%s", ctx.AgentName(), normalizeRuntimeLogString(modelName, "default"))
	return nil, nil
}

func (l *runtimeLogger) previewModelInput(ctx agent.CallbackContext, req *adkmodel.LLMRequest) (*adkmodel.LLMResponse, error) {
	modelName := ""
	if req != nil {
		modelName = req.Model
	}
	l.logf("[Model Input] agent=%s model=%s prompt=%s", ctx.AgentName(), normalizeRuntimeLogString(modelName, "default"), summarizeRequestForRuntimeLog(req, 240))
	return nil, nil
}

func (l *runtimeLogger) afterModel(ctx agent.CallbackContext, resp *adkmodel.LLMResponse, err error) (*adkmodel.LLMResponse, error) {
	if err != nil {
		l.logf("[Model] error agent=%s err=%v", ctx.AgentName(), err)
		return nil, nil
	}
	if resp == nil {
		return nil, nil
	}
	l.logf("[Model] done agent=%s partial=%t turn_complete=%t", ctx.AgentName(), resp.Partial, resp.TurnComplete)
	if !resp.Partial {
		l.logf("[Model Output] agent=%s content=%s", ctx.AgentName(), summarizeContentForRuntimeLog(resp.Content, 200))
	}
	if usage := formatUsageMetadataForRuntimeLog(resp.UsageMetadata); usage != "" {
		l.logf("[Tokens] source=model agent=%s %s", ctx.AgentName(), usage)
	}
	return nil, nil
}

func (l *runtimeLogger) onModelError(ctx agent.CallbackContext, _ *adkmodel.LLMRequest, err error) (*adkmodel.LLMResponse, error) {
	l.logf("[Model] error agent=%s err=%v", ctx.AgentName(), err)
	return nil, nil
}

func (l *runtimeLogger) beforeTool(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
	l.logf("[Tool] start name=%s agent=%s args=%s", t.Name(), ctx.AgentName(), formatMapForRuntimeLog(args, 240))
	return nil, nil
}

func (l *runtimeLogger) afterTool(ctx tool.Context, t tool.Tool, _ map[string]any, result map[string]any, err error) (map[string]any, error) {
	if err != nil {
		l.logf("[Tool] error name=%s agent=%s err=%v", t.Name(), ctx.AgentName(), err)
		return nil, nil
	}
	l.logf("[Tool] done name=%s agent=%s result=%s", t.Name(), ctx.AgentName(), formatMapForRuntimeLog(result, 240))
	return nil, nil
}

func (l *runtimeLogger) onToolError(ctx tool.Context, t tool.Tool, _ map[string]any, err error) (map[string]any, error) {
	l.logf("[Tool] error name=%s agent=%s err=%v", t.Name(), ctx.AgentName(), err)
	return nil, nil
}

func agentNameFromInvocation(ctx agent.InvocationContext) string {
	if ctx == nil || ctx.Agent() == nil {
		return "unknown"
	}
	return ctx.Agent().Name()
}

func summarizeContentForRuntimeLog(content *genai.Content, maxLength int) string {
	if content == nil || len(content.Parts) == 0 {
		return "empty"
	}
	var parts []string
	for _, part := range content.Parts {
		switch {
		case strings.TrimSpace(part.Text) != "":
			parts = append(parts, truncateForRuntimeLog(strings.TrimSpace(part.Text), maxLength))
		case part.FunctionCall != nil:
			parts = append(parts, "tool_call:"+part.FunctionCall.Name)
		case part.FunctionResponse != nil:
			parts = append(parts, "tool_result:"+part.FunctionResponse.Name)
		}
	}
	if len(parts) == 0 {
		return "non_text_content"
	}
	return strings.Join(parts, " | ")
}

func summarizeRequestForRuntimeLog(req *adkmodel.LLMRequest, maxLength int) string {
	if req == nil || len(req.Contents) == 0 {
		return "empty"
	}
	parts := make([]string, 0, len(req.Contents))
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		role := normalizeRuntimeLogString(content.Role, "unknown")
		parts = append(parts, role+":"+summarizeContentForRuntimeLog(content, maxLength))
	}
	if len(parts) == 0 {
		return "empty"
	}
	return truncateForRuntimeLog(strings.Join(parts, " || "), maxLength)
}

func formatMapForRuntimeLog(value map[string]any, maxLength int) string {
	if len(value) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return truncateForRuntimeLog(fmt.Sprintf("%v", value), maxLength)
	}
	return truncateForRuntimeLog(string(raw), maxLength)
}

func formatUsageMetadataForRuntimeLog(metadata *genai.GenerateContentResponseUsageMetadata) string {
	if metadata == nil {
		return ""
	}
	return fmt.Sprintf("input=%d output=%d total=%d", metadata.PromptTokenCount, metadata.CandidatesTokenCount, metadata.TotalTokenCount)
}

func truncateForRuntimeLog(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if maxLength <= 0 || len(value) <= maxLength {
		return value
	}
	if maxLength <= 3 {
		return value[:maxLength]
	}
	return value[:maxLength-3] + "..."
}

func normalizeRuntimeLogString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
