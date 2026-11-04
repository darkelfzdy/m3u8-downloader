package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// AppConfig 是持久化到配置文件中的全局默认配置。
// 字段保持与旧版本 config.json 完全兼容，新增字段均为可选。
type AppConfig struct {
	DefaultThreads  int                 `json:"default_threads"`
	TimeoutSec      int                 `json:"timeout_sec"`
	MaxRetries      int                 `json:"max_retries"`
	Proxy           string              `json:"proxy"`
	Insecure        bool                `json:"insecure"`
	DefaultDir      string              `json:"default_dir"`
	UserAgents      []string            `json:"user_agents"`
	AdvancedHeaders []map[string]string `json:"advanced_headers"`
}

// DownloadOptions 是一次下载任务的全部参数，由 CLI 参数串或 Web 输入框解析得到。
type DownloadOptions struct {
	URL      string   `json:"url"`
	OutName  string   `json:"name"`
	OutDir   string   `json:"dir"`
	Threads  int      `json:"threads"`
	Proxy    string   `json:"proxy"`
	Cookie   string   `json:"cookie"`
	Insecure bool     `json:"insecure"`
	KeepTs   bool     `json:"keep_ts"`
	Headers  []string `json:"headers"`
	Force    bool     `json:"force"`
	Retries  int      `json:"retries"`
	Stealth  bool     `json:"stealth"`
}

// DefaultConfig 返回内置默认配置，与旧版自动生成的 config.json 保持一致。
func DefaultConfig() AppConfig {
	defaultAdvancedHeader := map[string]string{
		"User-Agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
		"Accept":             "*/*",
		"Accept-Language":    "zh-CN,zh;q=0.9,en;q=0.8",
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "cross-site",
		"Sec-Ch-Ua":          `"Not;A=Brand";v="8", "Chromium";v="146", "Google Chrome";v="146"`,
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"Windows"`,
	}

	return AppConfig{
		DefaultThreads: 32,
		TimeoutSec:     15,
		MaxRetries:     5,
		Proxy:          "",
		Insecure:       false,
		DefaultDir:     "",
		UserAgents: []string{
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36",
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/121.0",
		},
		AdvancedHeaders: []map[string]string{defaultAdvancedHeader},
	}
}

// LoadConfig 读取配置文件。文件不存在时自动生成默认配置（保持旧版行为）。
func LoadConfig(path string) AppConfig {
	cfg := DefaultConfig()
	if path == "" {
		path = "config.json"
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if e := SaveConfig(path, cfg); e != nil {
				fmt.Printf("⚠️ 未检测到 %s，且自动生成失败: %v\n", path, e)
			} else {
				fmt.Printf("⚠️ 未检测到 %s，已自动生成默认配置文件。\n", path)
			}
		} else {
			fmt.Printf("⚠️ 读取配置文件失败: %v，使用默认配置。\n", err)
		}
		return cfg
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Printf("⚠️ 配置文件格式错误: %v，使用默认配置。\n", err)
		return DefaultConfig()
	}

	normalizeConfig(&cfg)
	return cfg
}

// SaveConfig 原子写入配置文件。
func SaveConfig(path string, cfg AppConfig) error {
	if path == "" {
		path = "config.json"
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeConfig(cfg *AppConfig) {
	if cfg.DefaultThreads <= 0 {
		cfg.DefaultThreads = 32
	}
	if cfg.DefaultThreads > 256 {
		cfg.DefaultThreads = 256
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 15
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
}

// ValidateConfig 校验并规范化 Web 端提交的配置。
func ValidateConfig(cfg *AppConfig) error {
	if cfg.DefaultThreads <= 0 {
		cfg.DefaultThreads = 32
	}
	if cfg.DefaultThreads > 256 {
		return fmt.Errorf("默认线程数不能超过 256")
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 15
	}
	if cfg.TimeoutSec > 600 {
		return fmt.Errorf("超时时间不能超过 600 秒")
	}
	if cfg.MaxRetries < 0 {
		return fmt.Errorf("重试次数不能为负数")
	}
	if cfg.MaxRetries > 100 {
		return fmt.Errorf("重试次数不能超过 100")
	}
	if cfg.UserAgents == nil {
		cfg.UserAgents = []string{}
	}
	if cfg.AdvancedHeaders == nil {
		cfg.AdvancedHeaders = []map[string]string{}
	}
	return nil
}

// GetRandomUA 从 UA 池中随机获取一个 User-Agent。
func GetRandomUA(cfg AppConfig) string {
	if len(cfg.UserAgents) == 0 {
		return "Mozilla/5.0"
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return cfg.UserAgents[r.Intn(len(cfg.UserAgents))]
}

// GetBaseHeaders 构造基础请求头。开启 -s 时使用完整浏览器特征头，否则只附带随机 UA。
func GetBaseHeaders(opts DownloadOptions, cfg AppConfig) map[string]string {
	headers := make(map[string]string)

	if opts.Stealth && len(cfg.AdvancedHeaders) > 0 {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		profile := cfg.AdvancedHeaders[r.Intn(len(cfg.AdvancedHeaders))]
		for k, v := range profile {
			headers[k] = v
		}
	} else {
		headers["User-Agent"] = GetRandomUA(cfg)
	}

	return headers
}
