package main

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/loong/uliya-go/tools/bashtool"
	"github.com/loong/uliya-go/tools/movetool"
	"github.com/loong/uliya-go/tools/todotool"
)

func TestWorkflowSupportsMultiTurnPathIntentAndConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "invoice.txt"), []byte("invoice"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	testModel := &workflowScriptModel{
		t:          t,
		targetPath: targetDir,
		intent:     "按文件名用途分类，按月份建二级目录",
	}

	bashTool, err := bashtool.New(targetDir)
	if err != nil {
		t.Fatalf("bashtool.New() error = %v", err)
	}

	rootAgent, err := newRootAgent(testModel, targetDir, nil, bashTool, todotool.New(), movetool.New())
	if err != nil {
		t.Fatalf("newRootAgent() error = %v", err)
	}

	sessionService := session.InMemoryService()
	testRunner, err := runner.New(runner.Config{
		AppName:           consoleAppName,
		Agent:             rootAgent,
		SessionService:    sessionService,
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	sessionID := "workflow-multi-turn"

	firstTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("请整理这个目录："+targetDir, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if !strings.Contains(firstTurn, "整理规则") {
		t.Fatalf("expected intent follow-up question, got %q", firstTurn)
	}

	storedAfterFirst, err := sessionService.Get(context.Background(), &session.GetRequest{
		AppName:   consoleAppName,
		UserID:    consoleUserID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("session.Get() after first turn error = %v", err)
	}
	if got := getStateString(storedAfterFirst.Session.State(), stateKeyPendingField); got != "intent" {
		t.Fatalf("expected pending intent after first turn, got %q", got)
	}

	secondTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText(testModel.intent, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("second turn error = %v", err)
	}
	if !strings.Contains(secondTurn, "计划已经生成") {
		t.Fatalf("expected confirmation plan after second turn, got %q", secondTurn)
	}

	storedAfterSecond, err := sessionService.Get(context.Background(), &session.GetRequest{
		AppName:   consoleAppName,
		UserID:    consoleUserID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("session.Get() after second turn error = %v", err)
	}
	if got := getStateString(storedAfterSecond.Session.State(), stateKeyAwaitingConfirmation); got != "true" {
		t.Fatalf("expected awaiting_confirmation=true after second turn, got %q", got)
	}
	if !strings.Contains(secondTurn, "invoice.txt -> 文档/2026-04/invoice.txt") {
		t.Fatalf("expected planned move in second turn output, got %q", secondTurn)
	}

	thirdTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("确认", genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("third turn error = %v", err)
	}
	if !strings.Contains(thirdTurn, "执行完成") {
		t.Fatalf("expected execution result after confirmation, got %q", thirdTurn)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "文档", "2026-04", "invoice.txt")); statErr != nil {
		t.Fatalf("expected organized file to exist, stat error = %v", statErr)
	}

	if !testModel.sawOrganizerIntent(testModel.intent) {
		t.Fatalf("expected organizer request to receive organization intent %q", testModel.intent)
	}
}

func TestWorkflowUsesShellPlanningForExtensionIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	targetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(targetDir, "invoice.txt"), []byte("invoice"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "photo.jpg"), []byte("photo"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	testModel := &workflowScriptModel{
		t:          t,
		targetPath: targetDir,
		intent:     "按扩展名整理",
	}

	bashTool, err := bashtool.New(targetDir)
	if err != nil {
		t.Fatalf("bashtool.New() error = %v", err)
	}

	rootAgent, err := newRootAgent(testModel, targetDir, nil, bashTool, todotool.New(), movetool.New())
	if err != nil {
		t.Fatalf("newRootAgent() error = %v", err)
	}

	sessionService := session.InMemoryService()
	testRunner, err := runner.New(runner.Config{
		AppName:           consoleAppName,
		Agent:             rootAgent,
		SessionService:    sessionService,
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	sessionID := "workflow-direct-extension"

	firstTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("整理目录："+targetDir, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if !strings.Contains(firstTurn, "整理规则") {
		t.Fatalf("expected intent follow-up question, got %q", firstTurn)
	}

	secondTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText(testModel.intent, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("second turn error = %v", err)
	}
	if !strings.Contains(secondTurn, "计划已经生成") {
		t.Fatalf("expected confirmation output, got %q", secondTurn)
	}
	if !strings.Contains(secondTurn, "find . -type f") {
		t.Fatalf("expected shell command plan, got %q", secondTurn)
	}
	if !strings.Contains(secondTurn, "txt: 1 file(s)") || !strings.Contains(secondTurn, "jpg: 1 file(s)") {
		t.Fatalf("expected extension notes in plan, got %q", secondTurn)
	}
	if !testModel.sawOrganizerSystemPrompt() {
		t.Fatal("expected organization agent to run for shell-based planning")
	}
	if !testModel.sawOrganizerTool("bash") {
		t.Fatal("expected bash tool to be available to the organization agent")
	}
	if !testModel.sawOrganizerTool("write_todo") {
		t.Fatal("expected write_todo to be available to the organization agent")
	}
	if !testModel.sawOrganizerTool("read_todo") {
		t.Fatal("expected read_todo to be available to the organization agent")
	}
	if testModel.sawOrganizerTool("list_files") || testModel.sawOrganizerTool("find_files") || testModel.sawOrganizerTool("read_file") {
		t.Fatal("did not expect filetools to be attached to the organization agent")
	}

	thirdTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("确认", genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("third turn error = %v", err)
	}
	if !strings.Contains(thirdTurn, "共运行 2 条 Shell 命令") {
		t.Fatalf("expected shell execution result, got %q", thirdTurn)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "按扩展名", "txt", "invoice.txt")); statErr != nil {
		t.Fatalf("expected txt file moved by shell command, stat error = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(targetDir, "按扩展名", "jpg", "photo.jpg")); statErr != nil {
		t.Fatalf("expected jpg file moved by shell command, stat error = %v", statErr)
	}
}

func TestWorkflowRejectsInvalidTargetPathBeforePlanning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	targetDir := filepath.Join(t.TempDir(), "missing-downloads")
	testModel := &workflowScriptModel{
		t:          t,
		targetPath: targetDir,
		intent:     "按扩展名整理",
	}

	bashTool, err := bashtool.New(t.TempDir())
	if err != nil {
		t.Fatalf("bashtool.New() error = %v", err)
	}

	rootAgent, err := newRootAgent(testModel, t.TempDir(), nil, bashTool, todotool.New(), movetool.New())
	if err != nil {
		t.Fatalf("newRootAgent() error = %v", err)
	}

	sessionService := session.InMemoryService()
	testRunner, err := runner.New(runner.Config{
		AppName:           consoleAppName,
		Agent:             rootAgent,
		SessionService:    sessionService,
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	sessionID := "workflow-invalid-path"

	firstTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("整理目录："+targetDir, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if !strings.Contains(firstTurn, "整理规则") {
		t.Fatalf("expected intent follow-up question, got %q", firstTurn)
	}

	secondTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText(testModel.intent, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("second turn error = %v", err)
	}
	if !strings.Contains(secondTurn, "目标目录不存在") {
		t.Fatalf("expected invalid-path error, got %q", secondTurn)
	}
	if strings.Contains(secondTurn, "计划已经生成") {
		t.Fatalf("did not expect confirmation plan for invalid path, got %q", secondTurn)
	}
	if testModel.sawOrganizerSystemPrompt() {
		t.Fatal("did not expect organization planner to run for an invalid target path")
	}

	stored, err := sessionService.Get(context.Background(), &session.GetRequest{
		AppName:   consoleAppName,
		UserID:    consoleUserID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("session.Get() error = %v", err)
	}
	if got := getStateString(stored.Session.State(), stateKeyPendingField); got != "path" {
		t.Fatalf("expected pending path after invalid path error, got %q", got)
	}
}

func TestWorkflowKeepsChineseFollowUpAfterPathOnlyReply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	targetDir := t.TempDir()
	testModel := &workflowLanguageModel{
		t:          t,
		targetPath: targetDir,
	}

	bashTool, err := bashtool.New(targetDir)
	if err != nil {
		t.Fatalf("bashtool.New() error = %v", err)
	}

	rootAgent, err := newRootAgent(testModel, targetDir, nil, bashTool, todotool.New(), movetool.New())
	if err != nil {
		t.Fatalf("newRootAgent() error = %v", err)
	}

	sessionService := session.InMemoryService()
	testRunner, err := runner.New(runner.Config{
		AppName:           consoleAppName,
		Agent:             rootAgent,
		SessionService:    sessionService,
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	sessionID := "workflow-language-follow-up"

	firstTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("我想整理下文件", genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if !strings.Contains(firstTurn, "请提供要整理的目录路径") {
		t.Fatalf("expected path follow-up question in Chinese, got %q", firstTurn)
	}

	secondTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText(targetDir, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("second turn error = %v", err)
	}
	if strings.Contains(secondTurn, "How would you like") {
		t.Fatalf("did not expect English follow-up after Chinese conversation, got %q", secondTurn)
	}
	if !strings.Contains(secondTurn, "你希望我按什么规则整理这些文件") {
		t.Fatalf("expected intent follow-up question in Chinese, got %q", secondTurn)
	}

	stored, err := sessionService.Get(context.Background(), &session.GetRequest{
		AppName:   consoleAppName,
		UserID:    consoleUserID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("session.Get() error = %v", err)
	}
	if got := getStateString(stored.Session.State(), stateKeyResponseLanguage); got != "zh" {
		t.Fatalf("expected response language to remain zh, got %q", got)
	}
}

func TestWorkflowGreetingThenPathAvoidsModelCalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	targetDir := t.TempDir()
	testModel := &workflowRejectingModel{t: t}

	bashTool, err := bashtool.New(targetDir)
	if err != nil {
		t.Fatalf("bashtool.New() error = %v", err)
	}

	rootAgent, err := newRootAgent(testModel, targetDir, nil, bashTool, todotool.New(), movetool.New())
	if err != nil {
		t.Fatalf("newRootAgent() error = %v", err)
	}

	sessionService := session.InMemoryService()
	testRunner, err := runner.New(runner.Config{
		AppName:           consoleAppName,
		Agent:             rootAgent,
		SessionService:    sessionService,
		AutoCreateSession: true,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}

	sessionID := "workflow-greeting-then-path"

	firstTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText("你好啊", genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("first turn error = %v", err)
	}
	if !strings.Contains(firstTurn, "请告诉我你要整理的目录路径和希望我按什么规则来整理") {
		t.Fatalf("expected local casual reply in Chinese, got %q", firstTurn)
	}

	secondTurn, err := collectRunText(testRunner.Run(context.Background(), consoleUserID, sessionID, genai.NewContentFromText(targetDir, genai.RoleUser), agent.RunConfig{}))
	if err != nil {
		t.Fatalf("second turn error = %v", err)
	}
	if !strings.Contains(secondTurn, "你希望我按什么规则整理这些文件") {
		t.Fatalf("expected local intent question in Chinese, got %q", secondTurn)
	}
}

type workflowScriptModel struct {
	t          *testing.T
	targetPath string
	intent     string
	requests   []*model.LLMRequest
}

func (m *workflowScriptModel) Name() string {
	return "workflow-script-model"
}

func (m *workflowScriptModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, req)
		respText, err := m.respond(req)
		if !yield(responseWithText(respText), err) {
			return
		}
	}
}

func (m *workflowScriptModel) respond(req *model.LLMRequest) (string, error) {
	contentsText := requestText(req.Contents)
	systemText := ""
	if req != nil && req.Config != nil {
		systemText = requestText([]*genai.Content{req.Config.SystemInstruction})
	}

	switch {
	case strings.Contains(contentsText, "Pending field: (none)") && strings.Contains(contentsText, m.targetPath):
		return fmt.Sprintf(`{"relevant":true,"path":%q,"intent":"","use_current_workspace":false}`, m.targetPath), nil
	case strings.Contains(contentsText, "Missing field: intent"):
		return "请告诉我整理规则。", nil
	case strings.Contains(contentsText, "Pending field: intent"):
		return fmt.Sprintf(`{"relevant":true,"path":"","intent":%q,"use_current_workspace":false}`, m.intent), nil
	case strings.Contains(systemText, "You are a file-organization planning agent."):
		if m.intent == "按扩展名整理" {
			return `{"summary":"按扩展名整理。","directories":["by_extension/jpg","by_extension/txt"],"moves":[],"commands":["find . -type f ! -path './by_extension/*' -iname '*.jpg' -print0 | while IFS= read -r -d '' path; do rel=${path#./}; dst=\"by_extension/jpg/$rel\"; mkdir -p \"$(dirname \"$dst\")\"; mv \"$rel\" \"$dst\"; done","find . -type f ! -path './by_extension/*' -iname '*.txt' -print0 | while IFS= read -r -d '' path; do rel=${path#./}; dst=\"by_extension/txt/$rel\"; mkdir -p \"$(dirname \"$dst\")\"; mv \"$rel\" \"$dst\"; done"],"notes":["jpg: 1 file(s)","txt: 1 file(s)"]}`, nil
		}
		return `{"summary":"按用途归类，并按月份放入二级目录。","directories":["Docs/2026-04"],"moves":[{"src":"invoice.txt","dst":"Docs/2026-04/invoice.txt","reason":"matches filename and time rule"}],"commands":[],"notes":[]}`, nil
	default:
		return "", fmt.Errorf("unexpected request: system=%q contents=%q", systemText, contentsText)
	}
}

func (m *workflowScriptModel) sawOrganizerIntent(intent string) bool {
	for _, req := range m.requests {
		if req == nil || req.Config == nil || req.Config.SystemInstruction == nil {
			continue
		}
		if strings.Contains(requestText([]*genai.Content{req.Config.SystemInstruction}), "Organization intent: "+intent) {
			return true
		}
	}
	return false
}

func (m *workflowScriptModel) sawOrganizerSystemPrompt() bool {
	for _, req := range m.requests {
		if req == nil || req.Config == nil || req.Config.SystemInstruction == nil {
			continue
		}
		systemText := requestText([]*genai.Content{req.Config.SystemInstruction})
		if strings.Contains(systemText, "You are a file-organization planning agent.") {
			return true
		}
	}
	return false
}

func (m *workflowScriptModel) sawOrganizerTool(name string) bool {
	for _, req := range m.requests {
		if req == nil || req.Tools == nil {
			continue
		}
		if _, ok := req.Tools[name]; ok {
			return true
		}
	}
	return false
}

type workflowLanguageModel struct {
	t          *testing.T
	targetPath string
	requests   []*model.LLMRequest
}

func (m *workflowLanguageModel) Name() string {
	return "workflow-language-model"
}

func (m *workflowLanguageModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.requests = append(m.requests, req)
		respText, err := m.respond(req)
		if !yield(responseWithText(respText), err) {
			return
		}
	}
}

func (m *workflowLanguageModel) respond(req *model.LLMRequest) (string, error) {
	contentsText := requestText(req.Contents)

	switch {
	case strings.Contains(contentsText, "Pending field: (none)") && strings.Contains(contentsText, "我想整理下文件"):
		return `{"relevant":true,"path":"","intent":"","use_current_workspace":false}`, nil
	case strings.Contains(contentsText, "Missing field: path"):
		return "请提供要整理的目录路径。", nil
	case strings.Contains(contentsText, "Pending field: path") && strings.Contains(contentsText, m.targetPath):
		return fmt.Sprintf(`{"relevant":true,"path":%q,"intent":"","use_current_workspace":false}`, m.targetPath), nil
	case strings.Contains(contentsText, "Missing field: intent"):
		if strings.Contains(contentsText, "Preferred reply language: zh") {
			return "你希望我按什么规则整理这些文件？", nil
		}
		return "How would you like the files in the directory organized?", nil
	default:
		return "", fmt.Errorf("unexpected request: contents=%q", contentsText)
	}
}

type workflowRejectingModel struct {
	t *testing.T
}

func (m *workflowRejectingModel) Name() string {
	return "workflow-rejecting-model"
}

func (m *workflowRejectingModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.t.Fatalf("did not expect model call during local intake flow: %q", requestText(req.Contents))
	}
}

func responseWithText(text string) *model.LLMResponse {
	return &model.LLMResponse{
		Content: genai.NewContentFromText(text, genai.RoleModel),
	}
}

func requestText(contents []*genai.Content) string {
	var builder strings.Builder
	for _, content := range contents {
		if content == nil {
			continue
		}
		builder.WriteString(contentPlainText(content))
		builder.WriteString("\n")
	}
	return builder.String()
}

func collectRunText(stream iter.Seq2[*session.Event, error]) (string, error) {
	var builder strings.Builder
	for event, err := range stream {
		if err != nil {
			return builder.String(), err
		}
		if event == nil || event.LLMResponse.Content == nil {
			continue
		}
		text := contentPlainText(event.LLMResponse.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
	}
	return builder.String(), nil
}
