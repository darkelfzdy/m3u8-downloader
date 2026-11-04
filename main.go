// @author: Orochi-Adde
// @date: 2026-07-25
// @功能: 高性能、低内存占用的 Golang M3U8 下载器（CLI + Web 双模式）
// @优化: 使用 tls_client (Chrome 146 指纹 + HTTP/2 HPACK 顺序伪装) 完美绕过 403 拦截，使用 -s 参数即可
// @优化: 标准 Worker Pool 并发，io.Copy 流式合并零内存泄露，支持断点续传与 AES-128 解密
// @优化: 新增 --serve 模式，提供 Docker + Web UI 图形化操作
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// version 可通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	if isServeMode() {
		runServer()
		return
	}
	runCLI()
}

func isServeMode() bool {
	if os.Getenv("SERVE") == "1" {
		return true
	}
	for _, a := range os.Args[1:] {
		if a == "--serve" || a == "-serve" {
			return true
		}
	}
	return false
}

// runCLI 保持原有命令行行为不变。
func runCLI() {
	cfg := LoadConfig("config.json")

	// 兼容直接粘贴纯 URL 的用法（与 Web 输入框行为一致）。
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if err := ValidateURL(args[0]); err == nil {
			args = append([]string{"-u", args[0]}, args[1:]...)
		}
	}

	opts, err := ParseOptions(args, cfg)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	if opts.URL == "" {
		log.Fatal("❌ 错误: 请提供有效的 M3U8 下载地址 (-u)")
	}
	if err := ValidateURL(opts.URL); err != nil {
		log.Fatalf("❌ 错误: %v", err)
	}

	d, err := NewDownloader(opts, cfg)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}

	// 1. 确定根目录 (最终 mp4 保存的地方)
	baseDir := opts.OutDir
	if baseDir == "" {
		baseDir, _ = os.Getwd()
	}
	os.MkdirAll(baseDir, os.ModePerm)

	// 2. 确定最终的 mp4 文件路径
	finalMoviePath := filepath.Join(baseDir, opts.OutName+".mp4")

	// 3. 建立专门的 ts 碎片临时文件夹 (加 _temp 后缀)
	tempTsDir := filepath.Join(baseDir, opts.OutName+"_temp")
	os.MkdirAll(tempTsDir, os.ModePerm)

	// 冲突检测：看最终的 mp4 是否已经存在
	if _, err := os.Stat(finalMoviePath); err == nil {
		if opts.Force {
			fmt.Printf("⚠️ 警告: 检测到已存在的成品文件 [%s.mp4]，将强行删除并重新下载。\n", opts.OutName)
			os.Remove(finalMoviePath)
		} else {
			log.Fatalf("❌ 文件冲突: 发现已合并完毕的 [%s.mp4]！\n如果您想重新下载覆盖，请加上 -f 或 -force 参数。", opts.OutName)
		}
	}

	printConfigBanner(opts, d.Headers())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m3u8Body, err := d.Fetch(ctx, opts.URL)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println("\n\n⚠️ 检测到终止信号 (Ctrl+C)，已保存当前断点进度，程序退出。下次启动将自动续传。")
			return
		}
		log.Fatalf("❌ 无法获取 M3U8: %v", err)
	}

	baseURL, err := url.Parse(opts.URL)
	if err != nil {
		log.Fatalf("❌ URL 非法: %v", err)
	}

	tsKey := d.GetM3u8Key(ctx, baseURL, string(m3u8Body))
	tsList := ParseTsList(baseURL, string(m3u8Body))

	fmt.Printf("➤ 发现视频碎片: %d 个\n", len(tsList))
	if tsKey != "" {
		fmt.Printf("➤ 解密状态: 已获取 AES-128 密钥\n")
	}
	fmt.Println("--------------------------------------------------")

	if err := d.DownloadAll(ctx, tsList, tempTsDir, tsKey, cliProgress); err != nil {
		fmt.Println()
		if errors.Is(err, context.Canceled) {
			fmt.Println("\n\n⚠️ 检测到终止信号 (Ctrl+C)，已保存当前断点进度，程序退出。下次启动将自动续传。")
			return
		}
		log.Fatalf("❌ 下载失败: %v", err)
	}
	fmt.Println()

	fmt.Println("\n\n🔧 下载完毕，正在进行流式无损合并...")
	if err := MergeTs(tsList, tempTsDir, finalMoviePath); err != nil {
		log.Fatalf("❌ 合并失败: %v", err)
	}

	if !opts.KeepTs {
		os.RemoveAll(tempTsDir)
		fmt.Println("🗑️ 临时碎片文件夹已清理")
	}

	fmt.Printf("✅ 搞定！文件保存在: %s\n", finalMoviePath)
}

func cliProgress(p DownloadProgress) {
	if p.Total <= 0 {
		return
	}
	percent := float32(p.Completed) / float32(p.Total)
	pos := int(percent * 30)
	if pos > 30 {
		pos = 30
	}
	fmt.Printf("\r[下载/校验中] %s%*s %6.2f%% | 速度: %5.2f MB/s | 完成: %d/%d ",
		strings.Repeat("■", pos), 30-pos, "", percent*100, p.Speed, p.Completed, p.Total)
}

func printConfigBanner(opts DownloadOptions, headers map[string]string) {
	proxyStatus := opts.Proxy
	if proxyStatus == "" {
		proxyStatus = "直连 (或跟随系统全局)"
	}
	fmt.Println("\n================ M3U8 Downloader ================")
	fmt.Printf("🎯 目标 URL  : %s\n", opts.URL)
	fmt.Printf("📂 保存名称  : %s.mp4\n", opts.OutName)
	fmt.Printf("🚀 并发线程  : %d\n", opts.Threads)
	fmt.Printf("🔄 错误重试  : %d 次\n", opts.Retries)
	fmt.Printf("🛡️ 强行覆盖  : %t\n", opts.Force)
	fmt.Printf("🕵️ 隐身模式(-s): %t\n", opts.Stealth)
	fmt.Printf("🌐 代理配置  : %s\n", proxyStatus)

	fmt.Println("--------------------------------------------------")
	fmt.Println("📋 [DEBUG] 当前生效的完整请求头 (Headers):")
	for k, v := range headers {
		fmt.Printf("    • %s: %s\n", k, v)
	}
	fmt.Println("==================================================")
}

// runServer 启动 Web 服务模式。
func runServer() {
	port := getenv("PORT", "8080")
	downloadDir := getenv("DOWNLOAD_DIR", "/downloads")
	configPath := getenv("CONFIG_PATH", "/config/config.json")
	webToken := os.Getenv("WEB_TOKEN")

	maxConcurrent, err := strconv.Atoi(getenv("MAX_CONCURRENT_TASKS", "1"))
	if err != nil || maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	cfg := LoadConfig(configPath)

	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		log.Fatalf("❌ 无法创建下载目录 %s: %v", downloadDir, err)
	}

	manager := NewManager(cfg, configPath, downloadDir, maxConcurrent)
	manager.Start()

	server := NewServer(manager, configPath, webToken, version)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("正在关闭服务...")
		manager.save()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	log.Printf("M3U8 Downloader Web 已启动: http://0.0.0.0:%s (版本 %s)", port, version)
	log.Printf("下载目录: %s | 配置文件: %s | 并发任务数: %d | 访问令牌: %s",
		downloadDir, configPath, maxConcurrent, tokenStatus(webToken))

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("❌ HTTP 服务启动失败: %v", err)
	}
}

func tokenStatus(token string) string {
	if token == "" {
		return "未启用"
	}
	return "已启用"
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
