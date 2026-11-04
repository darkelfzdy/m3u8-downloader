# 🚀 高性能 M3U8 流媒体下载器 (M3U8 Downloader)

基于 Go 语言打造的极速、轻量级流媒体视频下载工具。专为突破现代视频网站的反爬机制而生，拥有极高的并发性能和极低的内存占用。

- **CLI 模式**：保持原有命令行用法完全不变，适合脚本化调用。
- **Web 模式**：通过 Docker 一键部署，浏览器完成粘贴参数、预览、下载、查看进度与取回文件。

> 浏览器指纹下载功能：添加 `-s` 参数即可启用 Chrome 146 指纹与 HTTP/2 Header 顺序伪装。
>
> 专用油猴脚本：[m3u8-sniffer](https://raw.githubusercontent.com/darkelfzdy/m3u8-downloader/main/m3u8-sniffer.user.js)

---

## 🐳 Docker 快速开始（Web 版）

### 1. docker compose（推荐）

新建 `docker-compose.yml`：

```yaml
services:
  m3u8-downloader:
    image: darkelfzdy/m3u8-downloader:latest
    container_name: m3u8-downloader
    ports:
      - "8080:8080"
    volumes:
      - /vol1/1000/Downloads:/downloads   # 成品与临时碎片目录，按需修改
      - ./config:/config                  # 配置文件与任务记录持久化
    environment:
      - TZ=Asia/Shanghai
      - PUID=1000
      - PGID=1000
    restart: unless-stopped
```

启动：

```bash
docker compose up -d
```

浏览器打开 `http://NAS_IP:8080` 即可使用。

### 2. docker run

```bash
docker run -d \
  --name m3u8-downloader \
  -p 8080:8080 \
  -v /vol1/1000/Downloads:/downloads \
  -v $(pwd)/config:/config \
  -e TZ=Asia/Shanghai \
  -e PUID=1000 \
  -e PGID=1000 \
  --restart unless-stopped \
  darkelfzdy/m3u8-downloader:latest
```

### 3. 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | HTTP 监听端口 |
| `DOWNLOAD_DIR` | `/downloads` | 成品与临时目录根路径 |
| `CONFIG_PATH` | `/config/config.json` | 全局配置文件路径 |
| `PUID` / `PGID` | `1000` | 容器内运行用户，启动脚本负责切换 |
| `MAX_CONCURRENT_TASKS` | `1` | 同时运行的任务数 |
| `WEB_TOKEN` | 空 | 非空时，所有 `/api` 接口需要令牌 |
| `TZ` | `Asia/Shanghai` | 时区 |

> 安全提示：默认监听 `0.0.0.0:8080` 且无鉴权。请勿直接暴露公网；如需公网访问，务必设置 `WEB_TOKEN` 并配合反向代理与 HTTPS。

### 4. Web 界面功能

- **新建任务**：直接粘贴命令行参数，例如 `-u "https://example.com/video.m3u8" -o myvideo -n 64 -s`；也可以只粘贴纯 M3U8 地址，系统会自动补全 `-u`。
- **解析预览**：只解析 M3U8，返回碎片数与加密状态，不启动下载。
- **任务列表**：名称、状态、进度、速度、大小、创建时间；支持取消、失败重试、删除记录（可选删除文件）。
- **实时进度**：通过 SSE 推送，刷新页面后自动重新订阅。
- **断点续传**：容器重启后历史任务仍可见，未完成任务可点击重试继续下载。
- **文件列表**：展示下载目录中的成品文件，支持浏览器直接下载。
- **设置**：编辑默认线程数、超时、重试次数、代理、UA 池、高级请求头。

### 5. 访问令牌

设置 `WEB_TOKEN=your-token` 后，页面右上角齿轮 → 填写访问令牌即可。命令行调用示例：

```bash
curl -H "Authorization: Bearer your-token" http://127.0.0.1:8080/api/tasks
```

### 6. 国内镜像加速

Docker Hub 国内拉取较慢时，可为 Docker 配置阿里云/腾讯云镜像加速器，或使用支持 Docker Hub 同步的镜像站。镜像名保持 `darkelfzdy/m3u8-downloader`。

### 7. Nginx 反向代理（SSE）

SSE 需要关闭代理缓冲，否则进度不会实时更新：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_buffering off;
    proxy_cache off;
    proxy_read_timeout 3600s;
}
```

---

## ✨ 核心特性（CLI）

* ⚡️ **极速并发**：基于 Go 原生 Goroutine (Worker Pool 模型)，榨干宽带极限。
* 🛡️ **强力反爬**：
    * 内置海量真实浏览器 User-Agent 随机池。
    * 支持自动剥离伪装成 `.jpg/.png` 的图片头部，精准提取视频流（0x47 同步字节检测）。
    * 完美支持标准 AES-128 视频流自动解密。
* 🔄 **完美断点续传**：随时按 `Ctrl+C` 安全退出。再次运行相同命令，将以秒级速度校验已下载碎片并无缝续传。
* 📦 **零内存合并**：摒弃传统将所有碎片读入内存的合并方式，采用 `io.Copy` 硬盘级流式拷贝，合并 100GB 视频内存占用也极低。
* ⚙️ **智能配置分离**：支持 `config.json` 兜底配置与命令行临时参数双重控制（命令行优先级最高）。
* 🌐 **全能网络代理**：全面支持 HTTP/HTTPS/SOCKS5 代理配置，支持强制跳过 HTTPS 证书校验。

---

## 🛠️ CLI 快速开始

### 1. 首次运行与环境初始化

将下载好的压缩包解压，在当前目录下打开命令行终端。

直接运行任意命令或双击程序，如果程序检测不到配置文件，会**自动在当前目录生成一个排版整齐的 `config.json`**。

### 2. 基础下载

只需提供一个 `-u` 参数即可开始下载：

```bash
m3u8-downloader -u "https://example.com/video.m3u8"
```

也可以直接粘贴纯 URL：

```bash
m3u8-downloader "https://example.com/video.m3u8"
```

### 3. 启动 Web 服务（本地）

```bash
./m3u8-downloader --serve
# 或
SERVE=1 ./m3u8-downloader
```

本地运行时建议显式指定目录：

```bash
DOWNLOAD_DIR=./downloads CONFIG_PATH=./config/config.json PORT=8080 ./m3u8-downloader --serve
```

---

## ⚙️ 配置文件 (config.json)

程序优先读取目录下的 `config.json` 作为默认配置。环境变量优先级高于配置文件。

```json
{
  "default_threads": 32,
  "timeout_sec": 15,
  "max_retries": 5,
  "proxy": "",
  "insecure": false,
  "default_dir": "",
  "user_agents": [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 ..."
  ],
  "advanced_headers": [
    {
      "Accept": "*/*",
      "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
      "Sec-Ch-Ua": "\"Not;A=Brand\";v=\"8\", \"Chromium\";v=\"146\", \"Google Chrome\";v=\"146\"",
      "Sec-Ch-Ua-Mobile": "?0",
      "Sec-Ch-Ua-Platform": "\"Windows\"",
      "Sec-Fetch-Dest": "empty",
      "Sec-Fetch-Mode": "cors",
      "Sec-Fetch-Site": "cross-site",
      "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
    }
  ]
}
```

## 🎛️ 命令行参数速查表

| 缩写 | 完整参数 | 说明 | 示例 |
| :--- | :--- | :--- | :--- |
| **`-u`** | `-url` | **[必填]** M3U8 播放列表的完整下载地址 | `-u "http://xxx/a.m3u8"` |
| **`-o`** | `-name` | 最终生成的视频文件名（无需加 .mp4 后缀） | `-o "阿凡达"` |
| **`-n`** | `-threads` | 临时指定并发下载的线程数 | `-n 64` |
| **`-d`** | `-dir` | 视频及缓存碎片的保存目录（默认在当前目录） | `-d "D:\Downloads"` |
| **`-p`** | `-proxy` | 临时指定代理，**会覆盖配置文件的设置** | `-p "http://127.0.0.1:7890"` |
| **`-H`** | `-header` | 自定义 HTTP 请求头，**可多次叠加使用** | `-H "Referer: https://abc.com"` |
| **`-c`** | `-cookie` | 自定义 Cookie，用于需要登录的鉴权视频 | `-c "session_id=123; uid=456"` |
| **`-k`** | `-insecure` | 强制跳过 HTTPS 证书验证 | `-k` |
| **`-f`** | `-force` | **强行覆盖**：若存在同名 mp4，直接删除并重新下载 | `-f` |
| **`-r`** | `-retries` | 临时指定单碎片最大重试次数 | `-r 10` |
| **`-s`** | `-s` | 启用浏览器指纹伪装 | `-s` |
| 无 | `-keep` | 合并完 `.mp4` 后，保留 `.ts` 碎片不删除 | `-keep` |
| 无 | `--serve` | 启动 Web 服务模式 | `--serve` |

## 🎮 进阶使用场景

* **场景1：极限榨干网速**，千兆宽带可拉起 64 甚至 128 线程：

```bash
m3u8-downloader -u "https://example.com/video.m3u8" -o "高清大片" -n 64
```

* **场景2：突破防盗链**，使用 `-H` 无限叠加请求头：

```bash
m3u8-downloader -u "https://example.com/video.m3u8" -o "突破防盗链" -H "Referer: https://example.com" -H "Origin: https://example.com" -c "vip_token=xxxx"
```

* **场景3：临时走代理**：

```bash
m3u8-downloader -u "https://wall.com/video.m3u8" -p "socks5://127.0.0.1:10808"
```

反之，如果在 `config.json` 配置了全局代理，但想临时直连，可以使用 `-p ""` 清空代理。

* **场景4：强行洗牌重新下载**：

```bash
m3u8-downloader -u "https://example.com/video.m3u8" -o "老视频" -f
```

---

## 🔌 API 概览

统一前缀 `/api`，返回 JSON。错误格式：`{"error": "message"}`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/tasks` | 创建任务，body 为 `{"cmdline": "-u \"https://...\" -o name -n 32 -s"}` |
| `GET` | `/api/tasks` | 任务列表 |
| `GET` | `/api/tasks/{id}` | 任务详情 |
| `GET` | `/api/tasks/{id}/events` | SSE 进度流 |
| `POST` | `/api/tasks/{id}/cancel` | 取消任务 |
| `POST` | `/api/tasks/{id}/retry` | 重试失败任务 |
| `DELETE` | `/api/tasks/{id}` | 删除记录，`?delete_file=true` 同时删文件 |
| `POST` | `/api/probe` | 解析 M3U8，返回碎片数与加密信息，不下载 |
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

---

## ❓ 常见问题 (FAQ)

**Q1：下载报错 tls: bad record MAC 怎么办？**

**A**：这通常意味着遭到了 Cloudflare 等高级 WAF 的拦截，识别到了 Go 语言的 TLS 握手特征。解决办法：请务必开启代理，并在命令行中传入代理参数（如 `-p "http://127.0.0.1:7890"`），或加 `-s` 启用浏览器指纹伪装。

**Q2：按 Ctrl+C 退出后，下次怎么继续下载？**

**A**：什么都不用改！直接按上箭头调出上一次运行的完整命令，回车即可。程序会自动扫描目录下的碎片，恢复进度并继续下载。Web 模式下点击任务上的“重试”即可续传。

**Q3：为什么有些视频网站嗅探出来的 M3U8 链接提示解析失败/找不到碎片？**

**A**：有些网站采用 Master M3U8（主播放列表嵌套），里面包含不同清晰度的子 M3U8。需要将包含真实 `.ts` 碎片的子 M3U8 链接喂给程序。通常在浏览器 Network 面板里找体积最大、不断刷新的那个请求。

**Q4：Docker 镜像支持 ARM 吗？**

**A**：本期仅构建 `linux/amd64`。ARM 设备可使用 CLI 版本自行编译。

---

## 📦 自行构建

```bash
# CLI 二进制
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o m3u8-downloader .

# Docker 镜像
docker build -t darkelfzdy/m3u8-downloader:latest .
```

## 📄 License

[MIT](LICENSE)
