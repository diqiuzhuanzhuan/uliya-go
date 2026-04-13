# Repository Guidelines

## Project Structure & Module Organization
`uliya-go` is a Go module for a file-organization chat agent. `main.go` wires the CLI/Web launcher, model, and shared callbacks. `workflow.go` handles intake, confirmation, and execution handoff. `workflow_multiagent.go` contains the planning and execution helpers for the current single planning agent. `openaimodel/` holds the OpenAI-compatible adapter. Reusable tools live under `tools/` (`bashtool/`, `filetools/`, `movetool/`, `todotool/`). Tests stay beside the code they cover as `*_test.go`. Runtime state is local: sessions default to `data/sessions.db`, and move operations are logged in `~/.uliya_ops.json`.

## Build, Test, and Development Commands
- `go mod tidy` updates module dependencies.
- `go run .` starts the console chat flow.
- `go run . web api webui` starts the local Web UI on `http://localhost:8080`.
- `go test ./...` runs all unit tests.
- `go test . ./tools/todotool -run 'TestWorkflow|TestExecute|TestWriteTodos'` runs focused workflow/todo checks during refactors.
- `go test -cover ./...` checks coverage before opening a PR.
- `gofmt -w $(rg --files -g '*.go')` formats all Go files.

Before running locally, create config with `cp .env.example .env` and set `OPENAI_API_KEY`. Common overrides are `OPENAI_MODEL`, `OPENAI_BASE_URL`, and `SESSION_DB_PATH`.

## Coding Style & Naming Conventions
Use `gofmt` output as the source of truth for formatting; do not hand-align indentation. Keep package names lowercase and matched to directory names. Exported identifiers use `CamelCase`; internal helpers use `camelCase`. Keep workflow orchestration in top-level files, model transport code in `openaimodel/`, and tool-specific behavior inside `tools/<name>/`.

## Testing Guidelines
Add tests alongside the changed package and name them with clear behavior-driven cases, for example `TestLoadDotEnvSkipsExistingEnv`. Cover success and failure paths, especially for planning guardrails, shell command validation, todo state transitions, and execution flow. Use `t.TempDir()` for filesystem tests instead of writing into the repository tree. After each staged refactor, run focused tests first, then `go test ./...`.

## Commit & Pull Request Guidelines
Recent history uses Conventional Commit prefixes such as `feat:`. Follow the same style with short imperative subjects, for example `fix: validate todo transitions`. PRs should include a concise summary, any required env/config changes, and the exact test command you ran. Add screenshots only when changing Web UI behavior.

## Agent Architecture Notes
Current development is aligning the runtime with `deepagents`-style behavior: one planning agent, shell/file tools for cheap inspection, and todo-driven progress instead of hard-coded staged planner/reviewer pipelines. Planning must stay read-only; execution happens only after confirmation. Prefer shell metadata discovery such as `find`, `stat`, `sort`, and `uniq` over loading large file inventories into the model.

## Near-Term Refactor Plan
Next work should:
- add an `execute`-style shell entry point on top of the current `bash` tool,
- verify large real directories return a plan within the planning budget,
- introduce optional `task`-style delegation only for complex subtasks,
- keep each refactor stage covered by focused tests before full-suite validation.

## Security & Configuration Tips
Do not commit real secrets or populated `.env` files. Treat `data/` as local runtime output unless a fixture is intentionally added for tests.
