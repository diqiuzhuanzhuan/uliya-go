# uliya-go

基于 `adk-go` 的最小聊天 Agent 示例。

当前默认使用 OpenAI 模型。

当前默认使用 SQLite 保存会话，因此重启程序后仍然可以保留聊天历史。

当前版本支持两种运行方式：

- 命令行聊天
- 本地 Web 聊天界面

## 前置要求

- Go 1.24.4 或更高版本
- 一个可用的 OpenAI API Key

## 初始化依赖

```bash
go mod tidy
```

## 配置环境变量

```bash
cp .env.example .env
```

把 `YOUR_OPENAI_API_KEY` 替换成你自己的 key。

程序启动时会自动尝试读取当前目录下的 `.env`。

如果你更希望在当前 shell 里显式导出变量，也可以继续手动执行：

```bash
source .env
```

如果你不是直连 OpenAI，而是走兼容 OpenAI 协议的代理，也可以设置：

```bash
export OPENAI_BASE_URL="https://your-proxy.example/v1"
```

如果你本来环境里已经在用 `OPENAI_API_URL`，现在也兼容，不需要额外改代码：

```bash
export OPENAI_API_URL="https://your-proxy.example/v1"
```

默认模型是 `gpt-4.1-mini`，你也可以改成别的：

```bash
export OPENAI_MODEL="gpt-4.1"
```

会话默认保存在 `data/sessions.db`，你也可以自定义路径：

```bash
export SESSION_DB_PATH="data/sessions.db"
```

## 启动命令行聊天

```bash
go run .
```

在命令行模式里，支持这几个内置命令：

```text
/new
/reset
/exit
```

- `/new`：新开一个会话，旧会话仍然保留在数据库里
- `/reset`：删除当前会话，并立即开始一个全新的空会话
- `/exit`：退出程序

## 启动 Web 聊天界面

```bash
go run . web api webui
```

启动后访问 [http://localhost:8080](http://localhost:8080)。

## 代码说明

核心逻辑在 `main.go`：

- 使用自定义的 `openaimodel.New(...)` 创建 OpenAI 模型适配层
- 使用 `llmagent.New(...)` 创建一个简单的聊天 Agent
- 默认挂载了 `write_todo`，用于像 `deepagents` 一样维护会话级 todo list
- 默认挂载了专门的文件工具：`list_files`、`find_files`、`read_file`、`write_file`
- 默认挂载了一个 `bash` 工具，Agent 可以在当前仓库内执行受限的 Bash 命令来查看或修改文件
- 使用 ADK 自带的 SQLite session service 保存会话历史
- 在 console 模式下支持 `/new` 和 `/reset` 这样的会话控制命令
- 使用 `full.NewLauncher()` 同时支持 CLI 和 Web 运行模式

推荐优先使用专门的文件工具完成常见操作：

- `write_todo`
- `list_files`
- `find_files`
- `glob_files`
- `grep_text`
- `read_file`
- `edit_file`
- `write_file`

其中 `read_file` 现在参考了 `reference/deepagents` 的思路，支持按行分页读取；
`edit_file` 也采用了精确字符串替换的模式，适合做小范围、可控的修改；
并且和 `deepagents` 一样，必须先 `read_file` 再 `edit_file`。

`write_todo` 参考了 `reference/deepagents` 的 `write_todos` 语义：

- 它维护的是“当前完整 todo list”，每次更新都要重写整张列表
- todo 会保存在当前 session state 里，因此同一个会话中可以持续跟踪
- 适合 2 步及以上的任务，用来显式展示 `pending`、`in_progress`、`completed`

`list_files` 现在默认递归列出目录树；如果只想看当前一层，可以显式传 `recursive=false`。

路径规则现在是：

- 相对路径默认相对当前仓库根目录
- 如果用户明确提供绝对路径，也允许直接访问那个路径

`bash` 工具不是按命令名逐个注册的，而是一个通用命令执行工具。
也就是说，Agent 需要时可以在这个工具里直接执行常见命令，例如：

- `ls`
- `find . -name '*.go'`
- `rg 'TODO'`
- `cat README.md`
- `sed -n '1,80p' main.go`

如果你后面想继续扩展，可以在这个基础上加入：

- 自定义工具
- 多 Agent 协作
- 会话持久化
- RAG / 知识库检索
