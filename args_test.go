package main

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []string
		wantErr bool
	}{
		{
			name:  "普通参数",
			input: `-u https://example.com/a.m3u8 -o name -n 32`,
			want:  []string{"-u", "https://example.com/a.m3u8", "-o", "name", "-n", "32"},
		},
		{
			name:  "双引号包含空格",
			input: `-u "https://example.com/a b.m3u8" -H "Referer: https://a.com/x y"`,
			want:  []string{"-u", "https://example.com/a b.m3u8", "-H", "Referer: https://a.com/x y"},
		},
		{
			name:  "单引号包含空格",
			input: `-c 'session=1; uid=2' -o 'my video'`,
			want:  []string{"-c", "session=1; uid=2", "-o", "my video"},
		},
		{
			name:  "空引号产生空参数",
			input: `-o "" -c ''`,
			want:  []string{"-o", "", "-c", ""},
		},
		{
			name:  "反斜杠转义",
			input: `-o my\ video -H "X-A: a\"b"`,
			want:  []string{"-o", "my video", "-H", `X-A: a"b`},
		},
		{
			name:  "多余空白",
			input: "  -u   http://a.com/a.m3u8\t-o  x  ",
			want:  []string{"-u", "http://a.com/a.m3u8", "-o", "x"},
		},
		{
			name:    "未闭合双引号",
			input:   `-u "http://a.com/a.m3u8`,
			wantErr: true,
		},
		{
			name:    "未闭合单引号",
			input:   `-o 'name`,
			wantErr: true,
		},
		{
			name:    "反斜杠结尾",
			input:   `-o name\`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitArgs(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望错误，但得到 %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("未期望错误: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("结果不匹配\n期望: %#v\n实际: %#v", tc.want, got)
			}
		})
	}
}

func TestJoinArgsRoundTrip(t *testing.T) {
	cases := [][]string{
		{"-u", "https://example.com/a.m3u8", "-o", "name"},
		{"-u", "https://example.com/a b.m3u8", "-H", "Referer: https://a.com/x y"},
		{"-o", "远程 测试", "-c", "session=1; uid=2"},
		{"-H", `X-A: a"b`, "-p", `socks5://127.0.0.1:10808`},
		{"-o", ""},
	}
	for _, want := range cases {
		line := JoinArgs(want)
		got, err := SplitArgs(line)
		if err != nil {
			t.Fatalf("JoinArgs(%#v) = %q 解析失败: %v", want, line, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("往返结果不匹配\n参数: %#v\n命令行: %q\n实际: %#v", want, line, got)
		}
	}
}

func TestParseOptions(t *testing.T) {
	cfg := DefaultConfig()

	opts, err := ParseOptions([]string{
		"-u", "https://example.com/index.m3u8",
		"-o", "myvideo",
		"-n", "64",
		"-s",
		"-H", "Referer: https://example.com",
		"-H", "Origin: https://example.com",
	}, cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if opts.URL != "https://example.com/index.m3u8" {
		t.Errorf("URL 不匹配: %s", opts.URL)
	}
	if opts.OutName != "myvideo" {
		t.Errorf("OutName 不匹配: %s", opts.OutName)
	}
	if opts.Threads != 64 {
		t.Errorf("Threads 不匹配: %d", opts.Threads)
	}
	if !opts.Stealth {
		t.Errorf("Stealth 应为 true")
	}
	if len(opts.Headers) != 2 {
		t.Errorf("Headers 数量不匹配: %d", len(opts.Headers))
	}
}

func TestParseOptionsUnknownFlag(t *testing.T) {
	cfg := DefaultConfig()
	if _, err := ParseOptions([]string{"-u", "https://a.com/a.m3u8", "-x"}, cfg); err == nil {
		t.Fatal("期望未知参数报错")
	}
}

func TestParseCommandLinePlainURL(t *testing.T) {
	cfg := DefaultConfig()
	opts, err := ParseCommandLine(`https://example.com/index.m3u8 -o demo -n 8`, cfg)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if opts.URL != "https://example.com/index.m3u8" {
		t.Errorf("URL 不匹配: %s", opts.URL)
	}
	if opts.OutName != "demo" {
		t.Errorf("OutName 不匹配: %s", opts.OutName)
	}
	if opts.Threads != 8 {
		t.Errorf("Threads 不匹配: %d", opts.Threads)
	}
}

func TestParseCommandLineEmpty(t *testing.T) {
	cfg := DefaultConfig()
	if _, err := ParseCommandLine("   ", cfg); err == nil {
		t.Fatal("期望空参数报错")
	}
}

func TestValidateURL(t *testing.T) {
	valid := []string{
		"https://example.com/a.m3u8",
		"http://127.0.0.1:8080/a.m3u8",
	}
	for _, u := range valid {
		if err := ValidateURL(u); err != nil {
			t.Errorf("%s 应合法: %v", u, err)
		}
	}

	invalid := []string{
		"",
		"ftp://example.com/a.m3u8",
		"example.com/a.m3u8",
		"https://",
	}
	for _, u := range invalid {
		if err := ValidateURL(u); err == nil {
			t.Errorf("%s 应非法", u)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"video", "视频-01", "a.b.c"}
	for _, n := range valid {
		if err := validateName(n); err != nil {
			t.Errorf("%q 应合法: %v", n, err)
		}
	}

	invalid := []string{"", ".", "..", "../etc/passwd", `a\b`, "a/b", "a..b"}
	for _, n := range invalid {
		if err := validateName(n); err == nil {
			t.Errorf("%q 应非法", n)
		}
	}
}
