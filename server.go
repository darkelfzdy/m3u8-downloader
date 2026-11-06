package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

//go:embed web
var webAssets embed.FS

// Server 持有 HTTP 处理所需的依赖。
type Server struct {
	manager    *Manager
	configPath string
	webToken   string
	version    string
}

// NewServer 创建 HTTP 服务。
func NewServer(manager *Manager, configPath, webToken, version string) *Server {
	return &Server{
		manager:    manager,
		configPath: configPath,
		webToken:   webToken,
		version:    version,
	}
}

// Routes 注册全部路由并套用中间件。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/info", s.handleInfo)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/", s.handleTaskItem)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/files/", s.handleFileDownload)

	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	// 静态资源文件名固定，embed 更新后必须让浏览器重新校验，
	// 否则升级后可能仍加载缓存中的旧 app.js / style.css。
	mux.Handle("/", noCache(http.FileServer(http.FS(sub))))

	return logRequests(cors(s.auth(mux)))
}

// cors 允许浏览器扩展 / 跨域网页直接调用远程 API（例如油猴脚本）。
// 鉴权仍然可选：设置了 WEB_TOKEN 时，令牌可放在 Authorization 或 ?token=。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPI := strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz"
		if isAPI {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		// 预检请求在鉴权之前直接返回，否则带 Authorization 的跨域调用会被 401 拦掉。
		if isAPI && r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noCache 要求浏览器每次使用资源前向服务端确认（可命中 304）。
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// auth 对 /api/ 前缀启用可选的 Bearer Token 鉴权。
// SSE 的 EventSource 无法自定义请求头，因此也接受 ?token= 查询参数。
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.webToken == "" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		token := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			token = strings.TrimPrefix(h, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}

		if token != s.webToken {
			writeError(w, http.StatusUnauthorized, "访问令牌无效")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isPollingPath 判断是否为前端定时轮询接口，这类请求量大且没有信息量。
func isPollingPath(path string) bool {
	switch path {
	case "/api/tasks", "/api/files", "/api/info", "/api/config":
		return true
	}
	return false
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		// 跳过前端高频轮询，只保留任务操作、配置修改、SSE 连接等有意义的请求。
		if r.Method == http.MethodGet && isPollingPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Truncate(time.Millisecond))
	})
}
