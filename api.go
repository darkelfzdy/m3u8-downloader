package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": s.version})
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"tasks": s.manager.List()})
	case http.MethodPost:
		var req struct {
			Cmdline string `json:"cmdline"`
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "请求体格式错误")
			return
		}
		if strings.TrimSpace(req.Cmdline) == "" {
			writeError(w, http.StatusBadRequest, "请输入下载参数")
			return
		}
		task, err := s.manager.CreateTask(req.Cmdline)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, task)
	default:
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
	}
}

// handleTasksBatch 批量取消、重试或删除任务。
// 每个任务独立处理，单项失败不会影响其余项，逐项返回结果供前端提示。
func (s *Server) handleTasksBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}

	var req struct {
		Action     string   `json:"action"`
		IDs        []string `json:"ids"`
		DeleteFile bool     `json:"delete_file"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "未选择任务")
		return
	}
	if len(req.IDs) > 500 {
		writeError(w, http.StatusBadRequest, "一次最多操作 500 个任务")
		return
	}

	var fn func(string) error
	switch req.Action {
	case "cancel":
		fn = s.manager.Cancel
	case "retry":
		fn = s.manager.Retry
	case "delete":
		fn = func(id string) error { return s.manager.Delete(id, req.DeleteFile) }
	default:
		writeError(w, http.StatusBadRequest, "不支持的操作: "+req.Action)
		return
	}

	type itemResult struct {
		ID    string `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	results := make([]itemResult, 0, len(req.IDs))
	for _, id := range req.IDs {
		if id == "" {
			continue
		}
		err := fn(id)
		item := itemResult{ID: id, OK: err == nil}
		if err != nil {
			item.Error = err.Error()
		}
		results = append(results, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleTaskItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if strings.Trim(rest, "/") == "batch" {
		s.handleTasksBatch(w, r)
		return
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}

	id := parts[0]

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			task, ok := s.manager.Get(id)
			if !ok {
				writeError(w, http.StatusNotFound, "任务不存在")
				return
			}
			writeJSON(w, http.StatusOK, task.snapshot())
		case http.MethodDelete:
			deleteFile := r.URL.Query().Get("delete_file") == "true"
			if err := s.manager.Delete(id, deleteFile); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		}
		return
	}

	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "接口不存在")
		return
	}

	switch parts[1] {
	case "events":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		s.handleTaskEvents(w, r, id)
	case "cancel":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		if err := s.manager.Cancel(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
	case "retry":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "方法不允许")
			return
		}
		if err := s.manager.Retry(id); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
	default:
		writeError(w, http.StatusNotFound, "接口不存在")
	}
}

func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request, id string) {
	task, ok := s.manager.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "任务不存在")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "当前服务器不支持 SSE")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := task.subscribe()
	defer unsubscribe()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}

	var req struct {
		Cmdline string `json:"cmdline"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}

	opts, err := ParseCommandLine(req.Cmdline, s.manager.Config())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ValidateURL(opts.URL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	d, err := NewDownloader(opts, s.manager.Config())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	body, err := d.Fetch(ctx, opts.URL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "无法获取 M3U8: "+err.Error())
		return
	}

	baseURL, err := url.Parse(opts.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "URL 非法")
		return
	}

	tsList := ParseTsList(baseURL, string(body))
	writeJSON(w, http.StatusOK, map[string]any{
		"url":       opts.URL,
		"name":      opts.OutName,
		"total":     len(tsList),
		"encrypted": HasM3u8Key(string(body)),
	})
}

// FileEntry 是文件列表的一项。
type FileEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}

	dir := s.manager.DownloadDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取下载目录失败: "+err.Error())
		return
	}

	files := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if name == "" {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	if err := validateName(name); err != nil {
		writeError(w, http.StatusBadRequest, "非法文件名")
		return
	}

	dir := s.manager.DownloadDir()
	abs := filepath.Join(dir, name)

	rel, err := filepath.Rel(dir, abs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		writeError(w, http.StatusBadRequest, "非法路径")
		return
	}

	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}

	http.ServeFile(w, r, abs)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.manager.Config())
	case http.MethodPut:
		// 以当前配置为基底做部分更新，避免客户端只提交部分字段时把其余字段清零。
		cfg := s.manager.Config()

		var patch struct {
			DefaultThreads  *int                 `json:"default_threads"`
			TimeoutSec      *int                 `json:"timeout_sec"`
			MaxRetries      *int                 `json:"max_retries"`
			Proxy           *string              `json:"proxy"`
			Insecure        *bool                `json:"insecure"`
			DefaultDir      *string              `json:"default_dir"`
			UserAgents      *[]string            `json:"user_agents"`
			AdvancedHeaders *[]map[string]string `json:"advanced_headers"`
		}
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "请求体格式错误")
			return
		}
		if patch.DefaultThreads != nil {
			cfg.DefaultThreads = *patch.DefaultThreads
		}
		if patch.TimeoutSec != nil {
			cfg.TimeoutSec = *patch.TimeoutSec
		}
		if patch.MaxRetries != nil {
			cfg.MaxRetries = *patch.MaxRetries
		}
		if patch.Proxy != nil {
			cfg.Proxy = *patch.Proxy
		}
		if patch.Insecure != nil {
			cfg.Insecure = *patch.Insecure
		}
		if patch.DefaultDir != nil {
			cfg.DefaultDir = *patch.DefaultDir
		}
		if patch.UserAgents != nil {
			cfg.UserAgents = *patch.UserAgents
		}
		if patch.AdvancedHeaders != nil {
			cfg.AdvancedHeaders = *patch.AdvancedHeaders
		}

		if err := ValidateConfig(&cfg); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := SaveConfig(s.configPath, cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "保存配置失败: "+err.Error())
			return
		}
		s.manager.SetConfig(cfg)
		log.Printf("配置已更新: 线程 %d | 超时 %ds | 重试 %d", cfg.DefaultThreads, cfg.TimeoutSec, cfg.MaxRetries)
		writeJSON(w, http.StatusOK, cfg)
	default:
		writeError(w, http.StatusMethodNotAllowed, "方法不允许")
	}
}
