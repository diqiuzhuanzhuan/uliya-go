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
	"strings"
	"time"

	"google.golang.org/adk/session/database"
	"google.golang.org/adk/session"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/runner"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"google.golang.org/genai"

	"github.com/loong/uliya-go/openaimodel"
)

const (
	consoleUserID = "console_user"
	consoleAppName = "uliya_go"
)

func main() {
	ctx := context.Background()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("missing OPENAI_API_KEY, please export it before running")
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

	chatAgent, err := llmagent.New(llmagent.Config{
		Name:        "uliya_chat_agent",
		Model:       model,
		Description: "A simple chat agent built with adk-go and an OpenAI model.",
		Instruction: `你是 Uliya，一个友好、简洁、靠谱的 AI 助手。
你主要职责是和用户自然聊天、回答问题、解释概念，并在信息不足时先说明假设。
默认使用中文回答；如果用户改用英文，你也可以切换到英文。`,
	})
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
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
		AgentLoader:    agent.NewSingleLoader(chatAgent),
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
	fmt.Println("Commands: /new creates a new chat, /reset clears the current chat, /exit quits.")
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

			text := contentText(event.LLMResponse.Content)
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

func printConsoleHelp() {
	fmt.Println("Usage: go run . [console] [-streaming_mode none|sse] [-shutdown-timeout 2s]")
	fmt.Println()
	fmt.Println("Console commands:")
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

func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}

	var builder strings.Builder
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		builder.WriteString(part.Text)
	}
	return builder.String()
}
