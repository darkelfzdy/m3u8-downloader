package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	// 引入官方 net/http 的替代品 fhttp，支持精准控制 HTTP/2 协议栈和 Header 顺序
	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// TsInfo 定义单个 TS 视频切片的元数据结构。
type TsInfo struct {
	Name string
	URL  string
}

// DownloadProgress 是一次进度快照。
type DownloadProgress struct {
	Total     int
	Completed int32
	Bytes     int64
	Speed     float64
}

// Downloader 持有单个任务独立的 HTTP 客户端与请求头，避免全局状态互相污染。
type Downloader struct {
	client  tls_client.HttpClient
	headers map[string]string
	opts    DownloadOptions
	timeout time.Duration
}

// minRequestTimeout 是单次请求的最小超时，避免慢速源因默认值过小而频繁中断。
const minRequestTimeout = 60 * time.Second

// NewDownloader 根据任务参数初始化带浏览器指纹的 HTTP 客户端与请求头。
func NewDownloader(opts DownloadOptions, cfg AppConfig) (*Downloader, error) {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout < minRequestTimeout {
		timeout = minRequestTimeout
	}

	options := []tls_client.HttpClientOption{
		// Client 层超时与 context 超时保持一致，给慢速大碎片留足时间。
		tls_client.WithTimeoutSeconds(int(timeout.Seconds())),
		// 核心防风控点：强制指定 TLS 握手及 HTTP/2 帧特征为真实的 Chrome 146 浏览器
		tls_client.WithClientProfile(profiles.Chrome_146),
	}

	if opts.Insecure {
		options = append(options, tls_client.WithInsecureSkipVerify())
	}

	// 注入代理配置（优先使用任务指定的 -proxy，其次读取环境变量）
	if opts.Proxy != "" {
		options = append(options, tls_client.WithProxyUrl(opts.Proxy))
	} else if sysProxy := os.Getenv("HTTP_PROXY"); sysProxy != "" {
		options = append(options, tls_client.WithProxyUrl(sysProxy))
	}

	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("初始化底层网络引擎失败: %w", err)
	}

	headers := GetBaseHeaders(opts, cfg)

	if opts.Cookie != "" {
		headers["Cookie"] = opts.Cookie
	}
	for _, h := range opts.Headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	return &Downloader{client: client, headers: headers, opts: opts, timeout: timeout}, nil
}

// Headers 返回当前生效的请求头（只读用途）。
func (d *Downloader) Headers() map[string]string {
	return d.headers
}

// Fetch 发起带有完整浏览器特征与顺序控制的 HTTP 请求。
func (d *Downloader) Fetch(ctx context.Context, reqURL string) ([]byte, error) {
	// 用 context 再兜一层超时：即使底层 Client.Timeout 在 DNS 解析或连接阶段失效，
	// 也能保证请求不会无限期挂起。
	if d.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	var orderKeys []string
	for k, v := range d.headers {
		req.Header.Set(k, v)
		orderKeys = append(orderKeys, k)
	}

	// 【核心防风控点】：显式指定 HTTP/2 头部字段在 HPACK 压缩时的发送顺序。
	req.Header[http.HeaderOrderKey] = orderKeys

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ParseTsList 解析 M3U8 播放列表文本，提取出所有 TS 切片的下载直链。
func ParseTsList(baseURL *url.URL, body string) []TsInfo {
	var tsList []TsInfo
	lines := strings.Split(body, "\n")
	index := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		index++
		tsURL, err := url.Parse(line)
		if err != nil {
			continue
		}
		fullURL := baseURL.ResolveReference(tsURL).String()
		tsList = append(tsList, TsInfo{
			Name: fmt.Sprintf("%05d.ts", index),
			URL:  fullURL,
		})
	}
	return tsList
}

// findM3u8KeyURI 从 M3U8 中提取 AES-128 密钥地址。
func findM3u8KeyURI(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "#EXT-X-KEY") || !strings.Contains(line, "URI") {
			continue
		}
		start := strings.Index(line, `URI="`)
		if start < 0 {
			continue
		}
		start += len(`URI="`)
		end := strings.Index(line[start:], `"`)
		if end < 0 {
			continue
		}
		return line[start : start+end]
	}
	return ""
}

// HasM3u8Key 判断播放列表是否包含加密密钥声明。
func HasM3u8Key(body string) bool {
	return findM3u8KeyURI(body) != ""
}

// GetM3u8Key 检查并获取加密视频的 AES Key 秘钥内容。
func (d *Downloader) GetM3u8Key(ctx context.Context, baseURL *url.URL, body string) string {
	uri := findM3u8KeyURI(body)
	if uri == "" {
		return ""
	}
	keyURLObj, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	fullKeyURL := baseURL.ResolveReference(keyURLObj).String()
	keyBytes, err := d.Fetch(ctx, fullKeyURL)
	if err != nil {
		return ""
	}
	return string(keyBytes)
}

// DownloadAll 任务分发器：利用 Goroutine 池实现多线程并发下载。
// 单轮失败的碎片会被收集起来在后续轮次重试，避免个别慢速碎片拖垮整个任务。
// onProgress 每秒至少回调一次，用于 CLI 打印或 Web SSE 推送。
func (d *Downloader) DownloadAll(ctx context.Context, tsList []TsInfo, savePath string, key string, onProgress func(DownloadProgress)) error {
	total := len(tsList)
	if total == 0 {
		return fmt.Errorf("未解析到视频碎片")
	}

	var completed int32
	var bytes int64

	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		var lastBytes int64
		report := func() {
			if onProgress == nil {
				return
			}
			cur := atomic.LoadInt64(&bytes)
			speed := float64(cur-lastBytes) / 1024 / 1024
			lastBytes = cur
			onProgress(DownloadProgress{
				Total:     total,
				Completed: atomic.LoadInt32(&completed),
				Bytes:     cur,
				Speed:     speed,
			})
		}
		for {
			select {
			case <-stopProgress:
				report()
				return
			case <-ticker.C:
				report()
			}
		}
	}()

	pending := make([]TsInfo, len(tsList))
	copy(pending, tsList)

	const maxRounds = 3
	var lastErr error
	for round := 1; round <= maxRounds && len(pending) > 0; round++ {
		if round > 1 {
			log.Printf("第 %d 轮重试：剩余 %d 个碎片", round, len(pending))
			if err := sleepCtx(ctx, time.Duration(round)*2*time.Second); err != nil {
				break
			}
		}
		failed := d.downloadBatch(ctx, pending, savePath, key, &completed, &bytes)
		if len(failed) == 0 {
			pending = nil
			break
		}
		lastErr = fmt.Errorf("%d 个碎片下载失败", len(failed))
		pending = failed
	}

	close(stopProgress)
	<-progressDone

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(pending) > 0 {
		return fmt.Errorf("部分碎片下载失败（重试 %d 轮后仍失败 %d 个）: %v", maxRounds, len(pending), lastErr)
	}
	return nil
}

// downloadBatch 用固定大小的 worker 池下载一批碎片，返回其中仍失败的碎片。
func (d *Downloader) downloadBatch(ctx context.Context, list []TsInfo, savePath, key string, completed *int32, bytes *int64) []TsInfo {
	threads := d.opts.Threads
	if threads <= 0 {
		threads = 1
	}
	if threads > len(list) {
		threads = len(list)
	}

	jobs := make(chan TsInfo)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := make([]TsInfo, 0)

	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ts := range jobs {
				if ctx.Err() != nil {
					return
				}
				n, err := d.downloadTs(ctx, ts, savePath, key)
				if err != nil {
					log.Printf("碎片 %s 下载失败: %v", ts.Name, err)
					mu.Lock()
					failed = append(failed, ts)
					mu.Unlock()
					continue
				}
				atomic.AddInt32(completed, 1)
				atomic.AddInt64(bytes, n)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, ts := range list {
			select {
			case jobs <- ts:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()
	return failed
}

// downloadTs 负责单个 TS 切片的下载、断点续传检测、AES 解密及 TS 流对齐清洗。
// 采用 .part 临时文件 + rename 的方式写入，避免异常退出留下被误判为完整的半截碎片。
func (d *Downloader) downloadTs(ctx context.Context, ts TsInfo, savePath string, key string) (int64, error) {
	filePath := filepath.Join(savePath, ts.Name)

	if info, err := os.Stat(filePath); err == nil {
		if d.opts.Force {
			os.Remove(filePath)
		} else if info.Size() > 0 {
			return info.Size(), nil
		} else {
			os.Remove(filePath)
		}
	}

	retries := d.opts.Retries
	if retries <= 0 {
		retries = 1
	}

	var lastErr error
	for i := 0; i < retries; i++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		data, err := d.Fetch(ctx, ts.URL)
		if err != nil || len(data) == 0 {
			if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("响应内容为空")
			}
			if sleepErr := sleepCtx(ctx, 1*time.Second); sleepErr != nil {
				return 0, sleepErr
			}
			continue
		}

		if key != "" {
			decrypted, decErr := AesDecrypt(data, []byte(key))
			if decErr != nil {
				lastErr = decErr
				continue
			}
			data = decrypted
		}

		// TS 流清洗：寻找 TS 包同步字节 0x47，切掉前面的杂质垃圾数据。
		for j := 0; j < len(data); j++ {
			if data[j] == 71 {
				data = data[j:]
				break
			}
		}

		tmpPath := filePath + ".part"
		if err := os.WriteFile(tmpPath, data, 0666); err != nil {
			return 0, err
		}
		if err := os.Rename(tmpPath, filePath); err != nil {
			os.Remove(tmpPath)
			return 0, err
		}
		return int64(len(data)), nil
	}

	return 0, fmt.Errorf("碎片 %s 下载失败: %v", ts.Name, lastErr)
}

// MergeTs 将临时碎片按顺序流式合并为最终文件，内存占用恒定。
func MergeTs(tsList []TsInfo, tempDir, finalPath string) error {
	out, err := os.Create(finalPath)
	if err != nil {
		return err
	}

	buf := make([]byte, 32*1024)
	for _, ts := range tsList {
		f, err := os.Open(filepath.Join(tempDir, ts.Name))
		if err != nil {
			out.Close()
			os.Remove(finalPath)
			return fmt.Errorf("缺少碎片 %s: %w", ts.Name, err)
		}
		_, copyErr := io.CopyBuffer(out, f, buf)
		f.Close()
		if copyErr != nil {
			out.Close()
			os.Remove(finalPath)
			return copyErr
		}
	}

	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
