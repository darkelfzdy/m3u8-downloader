package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
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
	client        tls_client.HttpClient
	headers       map[string]string
	opts          DownloadOptions
	headerTimeout time.Duration // 连接 + TLS 握手 + 等待响应头的上限
	idleTimeout   time.Duration // 读取 body 时允许的最长无数据间隔
}

const (
	// minIdleTimeout 是 body 读空闲超时的下限，避免慢速源因配置过小而频繁中断。
	minIdleTimeout = 60 * time.Second
	// defaultHeaderTimeout 是连接与等待响应头的默认上限。
	defaultHeaderTimeout = 30 * time.Second
)

// NewDownloader 根据任务参数初始化带浏览器指纹的 HTTP 客户端与请求头。
func NewDownloader(opts DownloadOptions, cfg AppConfig) (*Downloader, error) {
	idleTimeout := time.Duration(cfg.TimeoutSec) * time.Second
	if idleTimeout < minIdleTimeout {
		idleTimeout = minIdleTimeout
	}

	options := []tls_client.HttpClientOption{
		// 关闭 tls-client 的整体硬超时（默认 30s 会切断正在稳定传输的慢速大碎片），
		// 改由 Fetch 内的「响应头超时 + body 读空闲超时」两段式看门狗控制。
		tls_client.WithTimeoutMilliseconds(0),
		// 核心防风控点：强制指定 TLS 握手及 HTTP/2 帧特征为真实的 Chrome 146 浏览器
		tls_client.WithClientProfile(profiles.Chrome_146),
	}

	if opts.Insecure {
		options = append(options, tls_client.WithInsecureSkipVerify())
	}

	// 注入代理配置（优先使用任务指定的 -proxy，其次读取环境变量）
	if opts.Proxy != "" {
		options = append(options, tls_client.WithProxyUrl(opts.Proxy))
		log.Printf("网络参数: 使用任务指定代理 %s", opts.Proxy)
	} else if sysProxy := os.Getenv("HTTP_PROXY"); sysProxy != "" {
		options = append(options, tls_client.WithProxyUrl(sysProxy))
		log.Printf("网络参数: 使用环境变量代理 HTTP_PROXY=%s", sysProxy)
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

	log.Printf("网络参数: 并发 %d | 响应头超时 %s | 读空闲超时 %s",
		opts.Threads, defaultHeaderTimeout, idleTimeout)

	return &Downloader{
		client:        client,
		headers:       headers,
		opts:          opts,
		headerTimeout: defaultHeaderTimeout,
		idleTimeout:   idleTimeout,
	}, nil
}

// Headers 返回当前生效的请求头（只读用途）。
func (d *Downloader) Headers() map[string]string {
	return d.headers
}

// errHeaderTimeout / errIdleTimeout 用于把看门狗超时与用户主动取消区分开。
var (
	errHeaderTimeout = errors.New("等待响应头超时")
	errIdleTimeout   = errors.New("读取响应体空闲超时")
)

// Fetch 发起带有完整浏览器特征与顺序控制的 HTTP 请求。
// 超时采用两段式看门狗：连接/响应头阶段受 headerTimeout 约束；
// body 读取阶段只要持续有数据就不会被切断，仅在连续 idleTimeout 无数据时失败。
func (d *Downloader) Fetch(ctx context.Context, reqURL string) ([]byte, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var timedOut atomic.Int32

	// 阶段一：从发起到拿到响应头，最多等 headerTimeout。
	headerTimer := time.AfterFunc(d.headerTimeout, func() {
		timedOut.Store(1)
		cancel()
	})

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		headerTimer.Stop()
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
	headerTimer.Stop()
	if err != nil {
		if timedOut.Load() == 1 {
			return nil, fmt.Errorf("%w(%s)", errHeaderTimeout, d.headerTimeout)
		}
		return nil, err
	}
	defer resp.Body.Close()

	// 若响应头恰好在超时边界返回，此时 reqCtx 已被取消，直接按响应头超时处理。
	if timedOut.Load() == 1 {
		return nil, fmt.Errorf("%w(%s)", errHeaderTimeout, d.headerTimeout)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	// 阶段二：body 读空闲看门狗。每次成功读到数据就重置计时器。
	idleTimer := time.AfterFunc(d.idleTimeout, func() {
		timedOut.Store(1)
		cancel()
	})
	defer idleTimer.Stop()

	data, err := io.ReadAll(&idleResetReader{r: resp.Body, timer: idleTimer, d: d.idleTimeout})
	if err != nil {
		if timedOut.Load() == 1 {
			return nil, fmt.Errorf("%w(%s 无数据)", errIdleTimeout, d.idleTimeout)
		}
		return nil, err
	}
	return data, nil
}

// idleResetReader 在每次读到数据后重置空闲计时器，用于实现「读空闲超时」。
type idleResetReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (r *idleResetReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(r.d)
	}
	return n, err
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
	// 最后一轮把并发压到 4，避免失败碎片继续满并发互抢带宽。
	const lastRoundThreads = 4
	var lastErr error
	for round := 1; round <= maxRounds && len(pending) > 0; round++ {
		threads := d.opts.Threads
		if round == maxRounds && threads > lastRoundThreads {
			threads = lastRoundThreads
		}
		if round > 1 {
			log.Printf("第 %d 轮重试：剩余 %d 个碎片，并发 %d", round, len(pending), threads)
			if err := sleepCtx(ctx, time.Duration(round)*2*time.Second); err != nil {
				break
			}
		}
		failed := d.downloadBatch(ctx, pending, savePath, key, &completed, &bytes, threads)
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
func (d *Downloader) downloadBatch(ctx context.Context, list []TsInfo, savePath, key string, completed *int32, bytes *int64, threads int) []TsInfo {
	if threads <= 0 {
		threads = d.opts.Threads
	}
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
			if i < retries-1 {
				if sleepErr := sleepCtx(ctx, retryBackoff(i)); sleepErr != nil {
					return 0, sleepErr
				}
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

// retryBackoff 返回第 n 次重试前的等待时间：指数增长 + ±10% 抖动，避免所有 worker 同时重试。
func retryBackoff(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n > 3 {
		n = 3
	}
	base := time.Duration(1<<uint(n)) * time.Second // 1s, 2s, 4s, 8s
	jitter := time.Duration(rand.Int63n(int64(base/5)+1)) - base/10
	return base + jitter
}
