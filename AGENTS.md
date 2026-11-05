# AGENTS.md

## 项目概览

单模块 Go 项目（`package main`，无子包），同时提供 CLI 与 Web 两种模式：

- CLI：`m3u8-downloader -u <url> ...`（`main.go:runCLI`）
- Web：`--serve` 或 `SERVE=1`，`main.go:runServer` 启动 HTTP 服务 + 任务队列
- 前端是 `web/` 下的纯静态 JS/CSS/HTML，经 `server.go` 的 `//go:embed web` 编进二进制，**没有 npm 或前端构建步骤**

项目已基本成型，后续以小步改进为主，避免大范围重构。

## 环境限制（本容器）

- **禁止执行任何 docker 命令**：opencode 和项目都跑在容器内，没有 docker CLI/daemon。
- **Go 默认不在 PATH**，需要手动准备（容器可能被重置）：
  ```bash
  curl -fsSL https://go.dev/dl/go1.24.1.linux-amd64.tar.gz -o /tmp/go.tgz
  mkdir -p /home/coder/sdk && tar -C /home/coder/sdk -xzf /tmp/go.tgz
  export PATH=/home/coder/sdk/go/bin:$PATH
  ```
  若 `/home/coder/sdk/go/bin/go` 已存在，直接用即可。
- 编译/测试**必须 `CGO_ENABLED=0`**，否则报 `stdlib.h: No such file or directory`（容器没有 libc 头文件）。

## 常用命令

```bash
export PATH=/home/coder/sdk/go/bin:$PATH CGO_ENABLED=0

go build -o /tmp/m3u8-downloader .     # 验证编译
go vet ./...                           # 静态检查
go test ./...                          # 只有 args_test.go，秒级完成
go test -run TestSplitArgs -v .        # 跑单个测试

# 本地起 Web（不要在容器里用 docker）
DOWNLOAD_DIR=./downloads CONFIG_PATH=./config/config.json PORT=8080 ./m3u8-downloader --serve
```

每次改完至少跑 `go build` + `go test ./...`。

## 架构要点

- `main.go`：模式切换、CLI 流程、Web 启动、环境变量读取。
- `args.go`：`SplitArgs`（Web 输入框的 shell 风格分词）+ `ParseOptions`（flag 定义，CLI/Web 共用）。**CLI 参数必须保持向后兼容**。
- `downloader.go`：tls-client 客户端（固定 `Chrome_146` 指纹 + HTTP/2 Header 顺序伪装）、M3U8 解析、AES-128、并发下载、流式合并。
- `task.go`：Manager 任务队列 / 状态机 / `tasks.json` 持久化 / SSE 广播；`api.go`：HTTP handlers；`server.go`：路由、鉴权、embed。
- `web/*`：手写前端，改完**必须重新 `go build`** 才会生效（embed 在编译期）。
- `docs/PRD.md` 是 Web 版需求文档，不是运行配置。

## Web 模式行为（容易踩坑）

- 环境变量：`PORT`(8080)、`DOWNLOAD_DIR`(/downloads)、`CONFIG_PATH`(/config/config.json)、`WEB_TOKEN`、`MAX_CONCURRENT_TASKS`(1)。
- Web 任务**忽略 `-d`**，成品统一写入 `DOWNLOAD_DIR`（见 `task.go:CreateTask`）。
- 任务记录是 `CONFIG_PATH` 同目录下的 `tasks.json`；服务重启后 `running/merging/queued` 会被标记为 `failed`（提示可重试续传），并**不会自动恢复下载**。
- 鉴权只作用于 `/api/`；SSE 的 `EventSource` 无法自定义 header，所以同时接受 `?token=`。
- `logRequests` 会跳过前端高频轮询（`/api/tasks`、`/api/files`、`/api/info`、`/api/config`），看不到这些日志是正常的。

## 网络/反爬注意

- 客户端 profile 写死 `profiles.Chrome_146`；`-s` 只是切换到 `config.json` 里的整套 `advanced_headers`。
- `downloader.go` 有 `minRequestTimeout = 60s` 下限：`config.json` 的 `timeout_sec` 小于 60 会被抬到 60。
- `AppConfig`/`config.json` 要保持旧版字段兼容，新增字段必须是可选（见 `config.go` 注释）。

## Git 约定（用户明确要求）

- **每次完成修改后直接提交并推送**：`git add -A && git commit -m "..." && git push origin main`。
- 分支 `main` 跟踪 `origin/main`（`darkelfzdy/m3u8-downloader`）；`upstream` 指向 `Orochi-Adde/m3u8-downloader`，**不要往 upstream push**。
- 提交信息沿用现有风格：`fix:` / `feat:` / `docs:` 前缀。
- 注意副作用：push 到 `main` 会触发 `.github/workflows/docker.yml` 自动构建并推送 Docker Hub 镜像。
- `.gitignore` 为空，别把编译产物、`downloads/`、`config/`、`*_temp/` 等提交进去。

## CI 说明

- 实际生效的是 `docker.yml`：push `main`/tag 时构建 `linux/amd64` 镜像并推送 Docker Hub。
- `release.yml` 只有 `workflow_dispatch`（push 触发被注释），且写死 Go 1.21，与 `go.mod` 的 `go 1.24.1` 不一致；改 CI 时注意这一点。
