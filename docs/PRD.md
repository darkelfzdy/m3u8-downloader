# PRD：M3U8 Downloader Web 版（Docker 发行）

| 项目 | 内容 |
| --- | --- |
| 文档版本 | v1.0 |
| 日期 | 2026-11-04 |
| 产品名称 | M3U8 Downloader Web |
| 仓库 | https://github.com/darkelfzdy/m3u8-downloader |
| 发行方式 | Docker Hub 镜像（linux/amd64） |
| 状态 | 待评审 |

---

## 1. 背景

当前项目是一个基于 Go 的纯命令行 M3U8 下载器，具备浏览器指纹伪装、AES-128 解密、断点续传、流式合并等能力，主要面向熟悉命令行的用户。

在实际使用场景中（尤其是 NAS 用户），目标用户往往不具备 SSH 操作能力，也不希望在终端里拼接一长串参数。项目此前讨论过飞牛 fnOS 的 `.fpk` 打包方案，但 fpk 存在开发门槛高、需测试设备、需注册开发者、生命周期脚本繁琐等问题。

因此本版本选择 **Docker + Web UI** 路线：把现有二进制封装为容器，通过浏览器提供图形化操作界面，降低使用门槛，同时保持项目开源、镜像通过 GitHub Actions 自动发布到 Docker Hub。

## 2. 目标与非目标

### 2.1 产品目标

1. 用户通过一条 `docker compose up -d` 即可在任意 x86 Linux/NAS 上运行本工具。
2. 用户通过浏览器完成：粘贴命令行参数串 → 解析预览 → 启动下载 → 查看实时进度 → 获取成品文件。
3. 保持现有 CLI 能力完全可用，Web 模式为增量能力，不破坏原有使用方式。
4. 镜像通过 GitHub Actions 自动构建并推送 Docker Hub，Release 流程与现有 GitHub Release 并存。

### 2.2 非目标（本期不做）

- 不做飞牛 `.fpk` 打包，不上架飞牛应用中心。
- 不做 ARM 架构镜像（仅 `linux/amd64`）。
- 不做多用户体系、登录注册、权限分级。
- 不做视频转码、字幕合成、播放器功能。
- 不做 DRM（Widevine 等）破解。
- 不做任务跨设备同步、远程 API 开放平台。

## 3. 用户与场景

### 3.1 目标用户

| 用户类型 | 特征 | 核心诉求 |
| --- | --- | --- |
| NAS 用户 | 有 Docker，熟悉 Compose，不懂 Go | 图形界面下载，文件落到 NAS 共享目录 |
| 普通桌面用户 | Windows/Mac 装 Docker Desktop | 不想敲命令，要进度条 |
| 原 CLI 用户 | 熟悉参数 | CLI 不能退化，可继续脚本化调用 |

### 3.2 核心场景

**场景 A：首次部署**
用户看到 README 的 Docker 段落，复制 compose 文件，修改下载目录挂载，执行 `docker compose up -d`，浏览器打开 `http://NAS_IP:8080`，看到界面。

**场景 B：下载加密视频**
用户从浏览器开发者工具或油猴脚本拿到 `.m3u8` 地址，在输入框中粘贴 `-u "https://.../index.m3u8" -o myvideo -n 64 -s`，点击开始。页面显示碎片总数、实时速度、完成百分比。下载完成后页面给出成品路径。

**场景 C：断点续传**
用户中途关闭容器或网络中断，重新进入页面，看到任务状态为“已中断”，点击继续，程序校验已下载碎片并只补齐缺失部分。

**场景 D：批量排队**
用户连续提交 3 个任务，任务进入队列，按顺序执行，页面可查看全部任务状态。

## 4. 功能需求

### 4.1 任务创建

本期不提供多字段表单。所有下载参数通过**单一命令行式输入框**传入，格式与 CLI 完全一致，用户只需把平时在终端里敲的参数粘贴进来。

| 编号 | 需求 | 优先级 |
| --- | --- | --- |
| F-01 | 提供单一输入框，接受与 CLI 相同的参数串，例如 `-u "https://example.com/video.m3u8" -o myvideo -n 64 -s` | P0 |
| F-02 | 支持直接粘贴纯 URL，系统自动补全 `-u` | P0 |
| F-03 | 后端实现 shell 风格分词，支持双引号、单引号包裹含空格的值（如 Cookie、自定义 Header） | P0 |
| F-04 | 复用现有 flag 定义解析参数，保证 Web 与 CLI 的参数名、别名、默认值、语义完全一致 | P0 |
| F-05 | 解析失败时返回明确错误（未知参数、缺少参数值、URL 非法），并在输入框下方展示 | P0 |
| F-06 | 输入框旁提供“解析预览”按钮，仅解析 M3U8 返回碎片数与加密状态，不启动下载 | P1 |
| F-07 | 记录最近 10 条成功提交的参数串，支持一键回填 | P2 |

技术说明：后端新增 `ParseOptions(args []string) (DownloadOptions, error)`，内部使用 `flag.NewFlagSet` 注册与 CLI 相同的参数集；分词器独立为 `args.go`，不引入第三方库。

### 4.2 任务管理

| 编号 | 需求 | 优先级 |
| --- | --- | --- |
| F-08 | 任务列表展示：名称、状态、进度、速度、大小、创建时间 | P0 |
| F-09 | 任务状态机：`排队中 / 下载中 / 合并中 / 已完成 / 失败 / 已取消` | P0 |
| F-10 | 支持取消运行中的任务，清理临时碎片目录 | P0 |
| F-11 | 支持删除任务记录（可选是否同时删除文件） | P1 |
| F-12 | 支持失败任务重试 | P1 |
| F-13 | 支持中断任务续传（复用已下载碎片） | P0 |
| F-14 | 并发任务数可配置，默认同时运行 1 个，其余排队 | P1 |
| F-15 | 任务记录持久化，容器重启后仍可查看历史 | P1 |

### 4.3 实时进度

| 编号 | 需求 | 优先级 |
| --- | --- | --- |
| F-16 | 通过 SSE 推送进度，每秒至少 1 次 | P0 |
| F-17 | 展示：已完成碎片数/总数、百分比、实时速度、已下载字节 | P0 |
| F-18 | 页面刷新后自动重新订阅进度，不丢失状态 | P0 |
| F-19 | 合并阶段单独展示“合并中”状态 | P1 |

### 4.4 文件与目录

| 编号 | 需求 | 优先级 |
| --- | --- | --- |
| F-20 | 成品统一保存到 `DOWNLOAD_DIR`（环境变量指定） | P0 |
| F-21 | 提供文件列表页，展示成品文件及大小 | P1 |
| F-22 | 提供成品文件下载链接（浏览器直接下载） | P1 |
| F-23 | 临时碎片目录命名为 `{name}_temp`，完成后自动清理 | P0 |
| F-24 | 目录遍历防护：禁止 `..` 及越界路径 | P0 |

### 4.5 配置管理

| 编号 | 需求 | 优先级 |
| --- | --- | --- |
| F-25 | 页面提供全局默认配置编辑（线程数、超时、UA 池、高级请求头） | P1 |
| F-26 | 配置持久化到 `CONFIG_PATH`，容器重建不丢失 | P1 |
| F-27 | 环境变量优先级高于配置文件 | P0 |

### 4.6 界面

| 编号 | 需求 | 优先级 |
| --- | --- | --- |
| F-28 | 单页应用，无需构建工具，纯 HTML/CSS/JS 内嵌到二进制 | P0 |
| F-29 | 适配桌面与移动端宽度 | P1 |
| F-30 | 深色主题为默认，风格简洁，与 NAS 管理界面协调 | P1 |
| F-31 | 关键操作（取消、删除）二次确认 | P1 |

## 5. 非功能需求

| 类别 | 要求 |
| --- | --- |
| 性能 | 单任务下载速度不低于 CLI 模式的 90%；100GB 文件合并阶段内存占用 < 50MB |
| 稳定性 | 容器连续运行 7 天无内存泄漏；异常退出后任务记录不损坏 |
| 兼容性 | Go 1.21+；Docker 20.10+；仅 linux/amd64 |
| 镜像体积 | 最终镜像 < 30MB（alpine 基础） |
| 启动时间 | 容器启动到 HTTP 可访问 < 3 秒 |
| 安全 | 默认监听 0.0.0.0:8080；容器内以非 root 用户运行；提供可选访问密码 |
| 可观测性 | 标准输出打印结构化日志；提供 `/healthz` 健康检查端点 |
| 国际化 | 界面仅提供中文，不预留英文版本 |

## 6. 技术方案

### 6.1 总体架构

```
┌─────────────────────────────────────────┐
│              Docker 容器                 │
│                                          │
│  ┌────────────┐    ┌──────────────────┐ │
│  │  Web UI    │───▶│  HTTP Server     │ │
│  │ (embed.FS) │◀───│  (net/http)      │ │
│  └────────────┘    └────────┬─────────┘ │
│                             │            │
│                    ┌────────▼─────────┐ │
│                    │  Task Manager    │ │
│                    │  (队列/状态机)    │ │
│                    └────────┬─────────┘ │
│                             │            │
│                    ┌────────▼─────────┐ │
│                    │  Downloader Core │ │
│                    │ (现有逻辑改造)    │ │
│                    └────────┬─────────┘ │
│                             │            │
│                    ┌────────▼─────────┐ │
│                    │ /downloads (卷)  │ │
│                    └──────────────────┘ │
└─────────────────────────────────────────┘
```

### 6.2 代码改造点

现有代码存在若干阻碍 Web 化的全局状态，需要重构：

| 文件 | 现状 | 改造 |
| --- | --- | --- |
| `main.go` | `main()` 直接执行下载，`LoadConfigAndFlags()` 无 URL 时 `log.Fatal` | 拆分为 `runCLI()` 与 `runServer()`；`--serve` 进入服务模式；参数解析不再强制要求 URL |
| `config.go` | `Flags`、`Config` 为全局单例，flag 只能解析一次 | 抽出 `DownloadOptions` 结构体与 `ParseOptions(args []string)`；CLI 与 Web 共用同一套 flag 定义；新增 `args.go` 做 shell 风格分词 |
| `downloader.go` | `downloadedBytes`、`completedCount` 为包级原子变量；`httpClient`、`headerMap` 全局共享 | 改为 `Task` 持有自己的计数器与客户端；`StartDownloader` 接受 `*Task` 与进度回调 |
| `downloader.go` | `speedMonitor` 直接 `fmt.Printf` 到终端 | 改为向 `Task` 写入进度快照，由 SSE 推送；CLI 模式保留终端输出 |
| `downloader.go` | `downloadTsWithRetry` 读取全局 `Flags.Force` | 改为读取 `Task.Options` |
| `crypto.go` | 无全局状态 | 不改动 |

### 6.3 任务模型

```go
type TaskStatus string

const (
    StatusQueued    TaskStatus = "queued"
    StatusRunning   TaskStatus = "running"
    StatusMerging   TaskStatus = "merging"
    StatusCompleted TaskStatus = "completed"
    StatusFailed    TaskStatus = "failed"
    StatusCancelled TaskStatus = "cancelled"
)

type Task struct {
    ID          string          `json:"id"`
    URL         string          `json:"url"`
    Name        string          `json:"name"`
    Status      TaskStatus      `json:"status"`
    Total       int             `json:"total"`
    Completed   int32           `json:"completed"`
    Bytes       int64           `json:"bytes"`
    Speed       float64         `json:"speed"`
    Error       string          `json:"error,omitempty"`
    Options     DownloadOptions `json:"options"`
    CreatedAt   time.Time       `json:"created_at"`
    FinishedAt  *time.Time      `json:"finished_at,omitempty"`

    cancel      context.CancelFunc
    subscribers map[chan ProgressEvent]struct{}
    mu          sync.RWMutex
}
```

任务 ID 使用 `time + 短随机串`，避免引入 UUID 依赖。

### 6.4 API 设计

统一前缀 `/api`，返回 JSON。错误格式：`{"error": "message"}`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/tasks` | 创建任务，body 为 `{"cmdline": "-u \"https://...\" -o name -n 32 -s"}` |
| `GET` | `/api/tasks` | 任务列表（不含内部字段） |
| `GET` | `/api/tasks/{id}` | 任务详情 |
| `GET` | `/api/tasks/{id}/events` | SSE 进度流 |
| `POST` | `/api/tasks/{id}/cancel` | 取消任务 |
| `POST` | `/api/tasks/{id}/retry` | 重试失败任务 |
| `DELETE` | `/api/tasks/{id}` | 删除记录，`?delete_file=true` 同时删文件 |
| `POST` | `/api/probe` | 解析参数串中的 M3U8 地址，返回碎片数与加密信息，不下载 |
| `GET` | `/api/files` | 列出下载目录中的成品 |
| `GET` | `/api/files/{name}` | 下载文件（带路径校验） |
| `GET` | `/api/config` | 读取全局配置 |
| `PUT` | `/api/config` | 更新全局配置 |
| `GET` | `/healthz` | 健康检查，返回 `ok` |

SSE 事件格式：

```
event: progress
data: {"id":"t_xxx","status":"running","total":1200,"completed":340,"speed":8.42,"bytes":123456789}

```

### 6.5 配置与环境变量

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | HTTP 监听端口 |
| `DOWNLOAD_DIR` | `/downloads` | 成品与临时目录根路径 |
| `CONFIG_PATH` | `/config/config.json` | 全局配置文件路径 |
| `PUID` / `PGID` | `1000` | 容器内运行用户，启动脚本负责切换 |
| `MAX_CONCURRENT_TASKS` | `1` | 同时运行的任务数 |
| `WEB_TOKEN` | 空 | 非空时，所有 API 需要 `Authorization: Bearer <token>` |
| `TZ` | `Asia/Shanghai` | 时区 |

配置文件沿用现有 `config.json` 结构，新增字段保持向后兼容（未知字段忽略）。

### 6.6 前端

- 单个 `index.html`，内联 CSS 与 JS，通过 `//go:embed web/*` 打包。
- 不引入 React/Vue 等框架，避免构建链路；使用原生 DOM 操作。
- 页面结构：顶部状态栏（服务状态、版本）+ 命令行参数输入框 + 任务列表 + 文件列表 + 设置抽屉。
- SSE 使用 `EventSource`，断线自动重连（浏览器原生行为）。
- 移动端使用 CSS Grid 自适应，最小支持 375px 宽度。

### 6.7 Dockerfile

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/m3u8-downloader .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && addgroup -g 1000 app && adduser -D -u 1000 -G app app
COPY --from=build /out/m3u8-downloader /usr/local/bin/m3u8-downloader
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENV PORT=8080 DOWNLOAD_DIR=/downloads CONFIG_PATH=/config/config.json TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/downloads", "/config"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:${PORT}/healthz || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["--serve"]
```

`entrypoint.sh` 负责根据 `PUID`/`PGID` 调整运行用户并 `exec` 主程序，保证挂载目录权限正确。

### 6.8 docker-compose 示例（写入 README）

```yaml
services:
  m3u8-downloader:
    image: darkelfzdy/m3u8-downloader:latest
    container_name: m3u8-downloader
    ports:
      - "8080:8080"
    volumes:
      - /vol1/1000/Downloads:/downloads
      - ./config:/config
    environment:
      - TZ=Asia/Shanghai
      - PUID=1000
      - PGID=1000
    restart: unless-stopped
```

### 6.9 CI/CD

新增 `.github/workflows/docker.yml`，与现有 `release.yml` 并存：

- 触发条件：`push` 到 `main`/`master` 构建 `latest`；推送 `v*` tag 构建版本号标签；支持 `workflow_dispatch`。
- 步骤：
  1. `actions/checkout@v4`
  2. `docker/setup-buildx-action@v3`
  3. `docker/login-action@v3`，使用 `secrets.DOCKERHUB_USERNAME`、`secrets.DOCKERHUB_TOKEN`
  4. `docker/metadata-action@v5` 生成标签：`latest`、`v1.2.3`、`sha-xxxxxxx`
  5. `docker/build-push-action@v6`，`platforms: linux/amd64`，启用 `cache-from/to: type=gha`
- 不在 PR 中推送镜像，仅构建校验。

需要在 GitHub 仓库 Secrets 中配置：
- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`（Docker Hub Access Token，非密码）

### 6.10 目录结构（改造后）

```
m3u8-downloader/
├── main.go              # runCLI / runServer 分流
├── server.go            # 路由注册、中间件、静态资源
├── task.go              # Task 模型、队列、状态机
├── api.go               # HTTP handler
├── downloader.go        # 核心下载逻辑（去全局化）
├── config.go            # 配置与 Options 构造
├── args.go              # shell 风格分词 + ParseOptions
├── crypto.go
├── web/
│   ├── index.html
│   ├── app.js
│   └── style.css
├── docker/
│   └── entrypoint.sh
├── Dockerfile
├── docker-compose.yml
├── docs/PRD.md
└── .github/workflows/
    ├── release.yml      # 现有，保持不变
    └── docker.yml       # 新增
```

## 7. 兼容性要求

- CLI 用法完全不变：`m3u8-downloader -u "..." -o name -n 32 -s` 行为与当前版本一致。
- 不传 `--serve` 且未设置 `SERVE=1` 时，按 CLI 模式运行。
- 容器默认 `CMD ["--serve"]`，因此 Docker 用户无需额外参数。
- 现有 `config.json` 可直接被新版本读取。

## 8. 里程碑

| 阶段 | 内容 | 产出 |
| --- | --- | --- |
| M1 | 核心去全局化：抽出 `DownloadOptions`，改造 `StartDownloader` 支持进度回调，CLI 回归测试通过 | 可编译，CLI 行为不变 |
| M2 | HTTP 服务 + 任务队列 + SSE + 基础 API | 本地 `go run . --serve` 可用 curl 操作 |
| M3 | 前端页面：新建任务、任务列表、实时进度、取消 | 浏览器可完成完整下载流程 |
| M4 | 文件列表、配置页、访问令牌、持久化 | 功能完整 |
| M5 | Dockerfile、entrypoint、compose 示例、本地镜像验证 | `docker run` 可用 |
| M6 | GitHub Actions 推送 Docker Hub，README 更新 | 镜像可公开拉取 |

## 9. 验收标准

1. 执行 README 中的 compose 示例，30 秒内可通过浏览器访问界面。
2. 在输入框中粘贴 `-u "https://.../index.m3u8" -n 64 -s`，能完成下载，成品出现在挂载目录，页面显示“已完成”。
3. 下载过程中刷新页面，进度条继续正常更新。
4. 下载 500MB 文件时，`docker stats` 显示内存占用低于 100MB。
5. 中断容器后重启，历史任务仍可见；未完成任务可续传并最终成功。
6. 取消任务后，临时碎片目录被清理。
7. 尝试通过 `/api/files/../../etc/passwd` 访问，返回 400。
8. CLI 模式执行原有命令，输出与旧版本一致。
9. GitHub 推送 tag 后，Docker Hub 出现对应版本标签镜像。
10. 镜像 `docker inspect` 显示以非 root 用户运行。

## 10. 风险与对策

| 风险 | 影响 | 对策 |
| --- | --- | --- |
| 去全局化改动引入回归 | CLI 用户受影响 | M1 阶段先加单元测试与手工回归清单，保持 CLI 输出格式 |
| `tls-client` 在 alpine 下运行异常 | 镜像不可用 | 优先验证 `CGO_ENABLED=0` 静态编译；异常则改用 `debian:bookworm-slim` |
| Docker Hub 国内拉取慢 | 用户体验差 | README 补充阿里云/腾讯云镜像加速说明 |
| 参数串分词或转义处理有误 | 用户提交的参数被错误解析 | 分词器覆盖双引号、单引号、转义与空值用例；解析错误时原样回显错误位置 |
| 无鉴权导致被滥用 | 安全风险 | 提供 `WEB_TOKEN`；README 明确建议不要直接暴露公网 |
| SSE 在反向代理后断流 | 进度不更新 | 响应头设置 `X-Accel-Buffering: no`、`Cache-Control: no-cache`；文档说明 Nginx 配置 |
| 多任务并发导致带宽争抢 | 下载变慢 | 默认并发 1，可配置；前端给出提示 |

## 11. 已确认事项

1. Docker Hub 与 GitHub 仓库名统一使用 `darkelfzdy/m3u8-downloader`。
2. 只推送 Docker Hub，不推送 GHCR。
3. 界面仅中文，不做英文版本。
4. 镜像不内置 `ffmpeg`/`yt-dlp`，不提供转码功能。
5. 仅构建 `linux/amd64` 镜像。
