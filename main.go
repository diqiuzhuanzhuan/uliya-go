package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/session/database"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/loong/uliya-go/tools/bashtool"
	"github.com/loong/uliya-go/tools/filetools"
	"github.com/loong/uliya-go/tools/movetool"
	"github.com/loong/uliya-go/openaimodel"
	"github.com/loong/uliya-go/tools/todotool"
)

const (
	consoleUserID  = "console_user"
	consoleAppName = "uliya_go"
)

func main() {
	ctx := context.Background()

	if err := loadDotEnv(".env"); err != nil {
		log.Fatalf("failed to load .env: %v", err)
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("missing OPENAI_API_KEY; set it in your environment or .env before running")
	}

	modelName := os.Getenv("OPENAI_MODEL")
	if modelName == "" {
		modelName = "gpt-4.1-mini"
	}

	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("OPENAI_API_URL")
	}

	model, err := openaimodel.New(modelName, openaimodel.Config{
		APIKey:  apiKey,
		BaseURL: baseURL,
	})
	if err != nil {
		log.Fatalf("failed to create model: %v", err)
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to get working directory: %v", err)
	}

	bashTool, err := bashtool.New(repoRoot)
	if err != nil {
		log.Fatalf("failed to create bash tool: %v", err)
	}
	fileTools, err := filetools.New(repoRoot)
	if err != nil {
		log.Fatalf("failed to create file tools: %v", err)
	}
	todoTools := todotool.New()
	moveTools := movetool.New()

	rootAgent, err := newRootAgent(model, repoRoot, fileTools, bashTool, todoTools, moveTools)
	if err != nil {
		log.Fatalf("failed to create root workflow agent: %v", err)
	}

	sessionDBPath := os.Getenv("SESSION_DB_PATH")
	if sessionDBPath == "" {
		sessionDBPath = "data/sessions.db"
	}

	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	sessionService, err := database.NewSessionService(
		sqlite.Open(sessionDBPath),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)},
	)
	if err != nil {
		log.Fatalf("failed to create session service: %v", err)
	}
	if err := database.AutoMigrate(sessionService); err != nil {
		log.Fatalf("failed to migrate session database: %v", err)
	}

	config := &launcher.Config{
		AgentLoader:    agent.NewSingleLoader(rootAgent),
		SessionService: sessionService,
	}

	if shouldRunConsole(os.Args[1:]) {
		if err := runConsole(ctx, config, os.Args[1:]); err != nil {
			log.Fatalf("run failed: %v", err)
		}
		return
	}

	l := full.NewLauncher()
	if err := l.Execute(ctx, config, os.Args[1:]); err != nil {
		log.Fatalf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

func shouldRunConsole(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "console" {
		return true
	}
	return strings.HasPrefix(args[0], "-")
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		if key == "" || os.Getenv(key) != "" {
			continue
		}

		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if err := os.Setenv(key, expandEnvValue(value)); err != nil {
			return fmt.Errorf("set %s from %s: %w", key, filepath.Base(path), err)
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func expandEnvValue(value string) string {
	return os.Expand(value, func(name string) string {
		return os.Getenv(name)
	})
}

func syncTodoAfterTool(ctx tool.Context, calledTool tool.Tool, args, result map[string]any, runErr error) (map[string]any, error) {
	if ctx == nil || calledTool == nil {
		return nil, nil
	}
	if calledTool.Name() == "write_todo" {
		return nil, nil
	}
	if runErr != nil {
		return nil, nil
	}
	if err := todotool.MarkRefreshNeeded(ctx.State(), calledTool.Name()); err != nil {
		return nil, err
	}
	return nil, nil
}

func todoReminderBeforeModel(ctx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
	if ctx == nil {
		return nil, nil
	}
	active, err := todotool.ActiveTodo(ctx.State())
	if err != nil {
		return nil, err
	}
	if active == nil {
		if err := ctx.State().Set("temp:todo_refresh_reminder", ""); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func runConsole(ctx context.Context, config *launcher.Config, args []string) error {
	consoleArgs := args
	if len(consoleArgs) > 0 && consoleArgs[0] == "console" {
		consoleArgs = consoleArgs[1:]
	}

	fs := flag.NewFlagSet("console", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var streamingMode string
	var shutdownTimeout time.Duration

	fs.StringVar(&streamingMode, "streaming_mode", "", "defines streaming mode (none|sse)")
	fs.DurationVar(&shutdownTimeout, "shutdown-timeout", 2*time.Second, "console shutdown timeout")
	fs.Bool("otel_to_cloud", false, "ignored in custom console mode")

	if err := fs.Parse(consoleArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printConsoleHelp()
			return nil
		}
		return fmt.Errorf("failed to parse console flags: %w", err)
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected console arguments: %v", fs.Args())
	}
	if streamingMode != "" && streamingMode != string(agent.StreamingModeNone) && streamingMode != string(agent.StreamingModeSSE) {
		return fmt.Errorf("invalid streaming_mode: %s", streamingMode)
	}

	rootAgent := config.AgentLoader.RootAgent()
	r, err := runner.New(runner.Config{
		AppName:         consoleAppName,
		Agent:           rootAgent,
		SessionService:  config.SessionService,
		ArtifactService: config.ArtifactService,
		MemoryService:   config.MemoryService,
		PluginConfig:    config.PluginConfig,
	})
	if err != nil {
		return fmt.Errorf("failed to create runner: %w", err)
	}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	currentSession, err := createSession(ctx, config.SessionService)
	if err != nil {
		return err
	}

	fmt.Printf("Current session: %s\n", currentSession.ID())
	fmt.Println("Commands: /new creates a new chat, /reset clears the current chat, /undo reverses the last execution, /exit quits.")
	fmt.Print("\nUser -> ")

	reader := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			<-shutdownCtx.Done()
			return nil
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Println("\nEOF detected, exiting...")
				return nil
			}
			return err
		}

		userInput := strings.TrimSpace(line)
		if userInput == "" {
			fmt.Print("User -> ")
			continue
		}

		switch userInput {
		case "/undo":
			if err := runUndo(); err != nil {
				fmt.Printf("Undo failed: %v\n", err)
			}
			fmt.Print("User -> ")
			continue
		case "/exit", "/quit":
			return nil
		case "/new":
			currentSession, err = createSession(ctx, config.SessionService)
			if err != nil {
				return err
			}
			fmt.Printf("\nStarted new session: %s\n", currentSession.ID())
			fmt.Print("User -> ")
			continue
		case "/reset":
			if err := resetSession(ctx, config.SessionService, currentSession.ID()); err != nil {
				return err
			}
			currentSession, err = createSession(ctx, config.SessionService)
			if err != nil {
				return err
			}
			fmt.Printf("\nReset complete. New session: %s\n", currentSession.ID())
			fmt.Print("User -> ")
			continue
		}

		mode := agent.StreamingMode(streamingMode)
		if mode == "" {
			if fi, statErr := os.Stdout.Stat(); statErr == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
				mode = agent.StreamingModeSSE
			} else {
				mode = agent.StreamingModeNone
			}
		}

		userMsg := genai.NewContentFromText(userInput, genai.RoleUser)
		fmt.Print("\nAgent -> ")

		prevText := ""
		for event, runErr := range r.Run(ctx, consoleUserID, currentSession.ID(), userMsg, agent.RunConfig{
			StreamingMode: mode,
		}) {
			if runErr != nil {
				fmt.Printf("\nAGENT_ERROR: %v\n", runErr)
				continue
			}
			if event == nil || event.LLMResponse.Content == nil {
				continue
			}

			text := contentConsoleOutput(event.LLMResponse.Content)
			if mode != agent.StreamingModeSSE {
				fmt.Print(text)
				continue
			}

			if !event.IsFinalResponse() {
				fmt.Print(text)
				prevText += text
				continue
			}

			if text != prevText {
				fmt.Print(text)
			}
			prevText = ""
		}
		fmt.Print("\nUser -> ")
	}
}

func runUndo() error {
	ops, err := movetool.LoadOperations()
	if err != nil {
		return fmt.Errorf("load operations: %w", err)
	}
	if len(ops) == 0 {
		fmt.Println("Nothing to undo.")
		return nil
	}

	fmt.Printf("Undoing %d operation(s)...\n", len(ops))
	var warnings []string
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.Op {
		case "move":
			if err := os.MkdirAll(filepath.Dir(op.Src), 0o755); err != nil {
				warnings = append(warnings, fmt.Sprintf("mkdir %s: %v", filepath.Dir(op.Src), err))
				continue
			}
			if err := os.Rename(op.Dst, op.Src); err != nil {
				warnings = append(warnings, fmt.Sprintf("move %s → %s: %v", op.Dst, op.Src, err))
			}
		case "create_dir":
			if err := os.Remove(op.Path); err != nil && !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("rmdir %s: %v (directory may not be empty)", op.Path, err))
			}
		}
	}

	if err := movetool.ClearLog(); err != nil {
		warnings = append(warnings, fmt.Sprintf("clear log: %v", err))
	}

	for _, w := range warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	fmt.Printf("Undo complete (%d operations reversed).\n", len(ops))
	return nil
}

func printConsoleHelp() {
	fmt.Println("Usage: go run . [console] [-streaming_mode none|sse] [-shutdown-timeout 2s]")
	fmt.Println()
	fmt.Println("Console commands:")
	fmt.Println("  /undo   reverse the last file-organization execution")
	fmt.Println("  /new    create a new chat session and keep previous sessions in the database")
	fmt.Println("  /reset  delete the current chat session and start a fresh one")
	fmt.Println("  /exit   quit the console")
	fmt.Println()
}

func createSession(ctx context.Context, sessionService interface {
	Create(context.Context, *session.CreateRequest) (*session.CreateResponse, error)
}) (session.Session, error) {
	resp, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName: consoleAppName,
		UserID:  consoleUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	return resp.Session, nil
}

func resetSession(ctx context.Context, sessionService interface {
	Delete(context.Context, *session.DeleteRequest) error
}, sessionID string) error {
	if err := sessionService.Delete(ctx, &session.DeleteRequest{
		AppName:   consoleAppName,
		UserID:    consoleUserID,
		SessionID: sessionID,
	}); err != nil {
		return fmt.Errorf("failed to reset session: %w", err)
	}
	return nil
}

func contentConsoleOutput(content *genai.Content) string {
	if content == nil {
		return ""
	}

	var builder strings.Builder
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		if part.Text != "" {
			builder.WriteString(part.Text)
		}
		if part.FunctionResponse != nil {
			builder.WriteString(formatFunctionResponseForConsole(part.FunctionResponse))
		}
	}
	return builder.String()
}

func contentPlainText(content *genai.Content) string {
	if content == nil {
		return ""
	}

	var builder strings.Builder
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		if part.Text != "" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func formatFunctionResponseForConsole(resp *genai.FunctionResponse) string {
	if resp == nil {
		return ""
	}
	if resp.Name != "write_todo" {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("\n\n[Todo List Updated]\n")

	if todoList, ok := resp.Response["todo_list"].(string); ok && strings.TrimSpace(todoList) != "" {
		builder.WriteString(todoList)
		builder.WriteString("\n")
		return builder.String()
	}

	if total, ok := resp.Response["total_items"]; ok {
		builder.WriteString(fmt.Sprintf("Total items: %v\n", total))
		return builder.String()
	}

	builder.WriteString("(todo list updated)\n")
	return builder.String()
}
