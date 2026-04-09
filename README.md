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
source .env
```

把 `YOUR_OPENAI_API_KEY` 替换成你自己的 key。

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
- 使用 ADK 自带的 SQLite session service 保存会话历史
- 在 console 模式下支持 `/new` 和 `/reset` 这样的会话控制命令
- 使用 `full.NewLauncher()` 同时支持 CLI 和 Web 运行模式

如果你后面想继续扩展，可以在这个基础上加入：

- 自定义工具
- 多 Agent 协作
- 会话持久化
- RAG / 知识库检索
