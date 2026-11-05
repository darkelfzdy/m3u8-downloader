package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// stringSliceValue 实现 flag.Value，支持 -H/-header 多次叠加。
type stringSliceValue struct {
	target *[]string
}

func (s stringSliceValue) String() string {
	if s.target == nil {
		return ""
	}
	return strings.Join(*s.target, ", ")
}

func (s stringSliceValue) Set(v string) error {
	*s.target = append(*s.target, v)
	return nil
}

// SplitArgs 按 shell 风格对参数串分词。
// 支持单引号、双引号包裹含空格的值，支持反斜杠转义。
func SplitArgs(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	hasToken := false
	runes := []rune(line)

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
			hasToken = true

		case inDouble:
			if r == '\\' {
				if i+1 >= len(runes) {
					return nil, fmt.Errorf("参数串在位置 %d 处以反斜杠结尾", i+1)
				}
				i++
				cur.WriteRune(runes[i])
				hasToken = true
			} else if r == '"' {
				inDouble = false
			} else {
				cur.WriteRune(r)
				hasToken = true
			}

		default:
			switch r {
			case ' ', '\t', '\n', '\r':
				if hasToken {
					args = append(args, cur.String())
					cur.Reset()
					hasToken = false
				}
			case '\'':
				inSingle = true
				hasToken = true
			case '"':
				inDouble = true
				hasToken = true
			case '\\':
				if i+1 >= len(runes) {
					return nil, fmt.Errorf("参数串在位置 %d 处以反斜杠结尾", i+1)
				}
				i++
				cur.WriteRune(runes[i])
				hasToken = true
			default:
				cur.WriteRune(r)
				hasToken = true
			}
		}
	}

	if inSingle {
		return nil, fmt.Errorf("参数串存在未闭合的单引号")
	}
	if inDouble {
		return nil, fmt.Errorf("参数串存在未闭合的双引号")
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args, nil
}

// ParseOptions 使用与 CLI 完全相同的 flag 定义解析参数。
func ParseOptions(args []string, cfg AppConfig) (DownloadOptions, error) {
	fs := flag.NewFlagSet("m3u8-downloader", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var o DownloadOptions
	defaultName := time.Now().Format("video_060102_15-04-05")

	threads := cfg.DefaultThreads
	if threads <= 0 {
		threads = 8
	}
	retries := cfg.MaxRetries

	fs.StringVar(&o.URL, "url", "", "M3U8下载地址")
	fs.StringVar(&o.URL, "u", "", "同 -url")

	fs.StringVar(&o.OutName, "name", defaultName, "保存的文件名")
	fs.StringVar(&o.OutName, "o", defaultName, "同 -name")

	fs.StringVar(&o.OutDir, "dir", cfg.DefaultDir, "保存目录")
	fs.StringVar(&o.OutDir, "d", cfg.DefaultDir, "同 -dir")

	fs.IntVar(&o.Threads, "threads", threads, "并发线程数")
	fs.IntVar(&o.Threads, "n", threads, "同 -threads")

	fs.StringVar(&o.Proxy, "proxy", cfg.Proxy, "设置代理")
	fs.StringVar(&o.Proxy, "p", cfg.Proxy, "同 -proxy")

	fs.BoolVar(&o.Insecure, "insecure", cfg.Insecure, "跳过 HTTPS 验证")
	fs.BoolVar(&o.Insecure, "k", cfg.Insecure, "同 -insecure")

	fs.StringVar(&o.Cookie, "cookie", "", "自定义 Cookie")
	fs.StringVar(&o.Cookie, "c", "", "同 -cookie")

	fs.BoolVar(&o.KeepTs, "keep", false, "保留 ts 碎片")

	fs.Var(stringSliceValue{&o.Headers}, "header", "自定义 HTTP 请求头")
	fs.Var(stringSliceValue{&o.Headers}, "H", "同 -header")

	fs.BoolVar(&o.Force, "force", false, "强行覆盖已存在的文件")
	fs.BoolVar(&o.Force, "f", false, "同 -force")

	fs.IntVar(&o.Retries, "retries", retries, "最大重试次数")
	fs.IntVar(&o.Retries, "r", retries, "同 -retries")

	fs.BoolVar(&o.Stealth, "s", false, "启用完整的浏览器环境特征 Header 伪装")

	if err := fs.Parse(args); err != nil {
		return DownloadOptions{}, fmt.Errorf("参数解析失败: %v", err)
	}
	if fs.NArg() > 0 {
		return DownloadOptions{}, fmt.Errorf("存在无法识别的多余参数: %s", strings.Join(fs.Args(), " "))
	}

	if o.Threads <= 0 {
		o.Threads = threads
	}
	if o.Threads > 256 {
		o.Threads = 256
	}
	if o.Retries <= 0 {
		o.Retries = retries
	}
	if o.OutName == "" {
		o.OutName = defaultName
	}

	return o, nil
}

// ParseCommandLine 解析 Web 输入框中的命令行式参数串。
// 支持直接粘贴纯 URL（自动补全 -u）。
func ParseCommandLine(cmdline string, cfg AppConfig) (DownloadOptions, error) {
	args, err := SplitArgs(cmdline)
	if err != nil {
		return DownloadOptions{}, err
	}
	if len(args) == 0 {
		return DownloadOptions{}, fmt.Errorf("请输入下载参数，例如: -u \"https://example.com/video.m3u8\" -o myvideo")
	}

	if !strings.HasPrefix(args[0], "-") {
		if err := ValidateURL(args[0]); err == nil {
			args = append([]string{"-u", args[0]}, args[1:]...)
		} else {
			return DownloadOptions{}, fmt.Errorf("无法识别的参数 %q，参数应以 - 开头，或直接粘贴完整的 http(s) 地址", args[0])
		}
	}

	return ParseOptions(args, cfg)
}

// ValidateURL 校验下载地址是否为合法的 http(s) URL。
func ValidateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("请提供有效的 M3U8 下载地址 (-u)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL 非法: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL 非法: 仅支持 http/https 协议")
	}
	if u.Host == "" {
		return fmt.Errorf("URL 非法: 缺少主机名")
	}
	return nil
}
