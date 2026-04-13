# Repository Guidelines

## Project Structure & Module Organization
`uliya-go` is a small Go module for a workflow-driven chat agent. `main.go` is the entry point for CLI and Web launch modes, and `workflow.go` plus `workflow_multiagent.go` contain request routing and multi-step execution logic. `openaimodel/` holds the OpenAI-compatible model adapter. Reusable tools live under `tools/` (`bashtool/`, `filetools/`, `movetool/`, `todotool/`). Tests stay next to the code they cover as `*_test.go`. Runtime state is local: sessions default to `data/sessions.db`, and move operations are logged in `~/.uliya_ops.json`.

## Build, Test, and Development Commands
- `go mod tidy` updates module dependencies.
- `go run .` starts the console chat flow.
- `go run . web api webui` starts the local Web UI on `http://localhost:8080`.
- `go test ./...` runs all unit tests.
- `go test -cover ./...` checks coverage before opening a PR.
- `gofmt -w $(rg --files -g '*.go')` formats all Go files.

Before running locally, create config with `cp .env.example .env` and set `OPENAI_API_KEY`. Common overrides are `OPENAI_MODEL`, `OPENAI_BASE_URL`, and `SESSION_DB_PATH`.

## Coding Style & Naming Conventions
Use `gofmt` output as the source of truth for formatting; do not hand-align indentation. Keep package names lowercase and matched to directory names. Exported identifiers use `CamelCase`; internal helpers use `camelCase`. Keep workflow orchestration in top-level files, model transport code in `openaimodel/`, and tool-specific behavior inside `tools/<name>/`.

## Testing Guidelines
Add tests alongside the changed package and name them with clear behavior-driven cases, for example `TestLoadDotEnvSkipsExistingEnv`. Cover both success and failure paths, especially for file edits, move planning, todo state transitions, and console input handling. Use `t.TempDir()` for filesystem tests instead of writing into the repository tree.

## Commit & Pull Request Guidelines
Recent history uses Conventional Commit prefixes such as `feat:`. Follow the same style with short imperative subjects, for example `fix: validate todo transitions`. PRs should include a concise summary, any required env/config changes, and the exact test command you ran. Add screenshots only when changing Web UI behavior.

## Security & Configuration Tips
Do not commit real secrets or populated `.env` files. Treat `data/` as local runtime output unless a fixture is intentionally added for tests.
