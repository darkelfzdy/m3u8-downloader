package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TaskStatus 任务状态机。
type TaskStatus string

const (
	StatusQueued    TaskStatus = "queued"
	StatusRunning   TaskStatus = "running"
	StatusMerging   TaskStatus = "merging"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCancelled TaskStatus = "cancelled"
)

// ProgressEvent 是通过 SSE 推送给前端的事件载荷。
type ProgressEvent struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     TaskStatus `json:"status"`
	Total      int        `json:"total"`
	Completed  int32      `json:"completed"`
	Speed      float64    `json:"speed"`
	Bytes      int64      `json:"bytes"`
	Error      string     `json:"error,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TaskView 是任务对外的只读快照，避免把内部锁暴露给 JSON 序列化。
type TaskView struct {
	ID         string          `json:"id"`
	URL        string          `json:"url"`
	Name       string          `json:"name"`
	Cmdline    string          `json:"cmdline"`
	Status     TaskStatus      `json:"status"`
	Total      int             `json:"total"`
	Completed  int32           `json:"completed"`
	Bytes      int64           `json:"bytes"`
	Speed      float64         `json:"speed"`
	Error      string          `json:"error,omitempty"`
	Options    DownloadOptions `json:"options"`
	CreatedAt  time.Time       `json:"created_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// Task 持有单个任务的全部运行时状态。
type Task struct {
	ID         string
	URL        string
	Name       string
	Cmdline    string
	Status     TaskStatus
	Total      int
	Completed  int32
	Bytes      int64
	Speed      float64
	Error      string
	Options    DownloadOptions
	CreatedAt  time.Time
	FinishedAt *time.Time

	cancel      context.CancelFunc
	subscribers map[chan ProgressEvent]struct{}
	mu          sync.RWMutex
}

func newTask(id, cmdline string, opts DownloadOptions) *Task {
	return &Task{
		ID:          id,
		URL:         opts.URL,
		Name:        opts.OutName,
		Cmdline:     cmdline,
		Status:      StatusQueued,
		Options:     opts,
		CreatedAt:   time.Now(),
		subscribers: make(map[chan ProgressEvent]struct{}),
	}
}

func (t *Task) snapshot() TaskView {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TaskView{
		ID:         t.ID,
		URL:        t.URL,
		Name:       t.Name,
		Cmdline:    t.Cmdline,
		Status:     t.Status,
		Total:      t.Total,
		Completed:  t.Completed,
		Bytes:      t.Bytes,
		Speed:      t.Speed,
		Error:      t.Error,
		Options:    t.Options,
		CreatedAt:  t.CreatedAt,
		FinishedAt: t.FinishedAt,
	}
}

func (t *Task) event() ProgressEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return ProgressEvent{
		ID:         t.ID,
		Name:       t.Name,
		Status:     t.Status,
		Total:      t.Total,
		Completed:  t.Completed,
		Speed:      t.Speed,
		Bytes:      t.Bytes,
		Error:      t.Error,
		FinishedAt: t.FinishedAt,
	}
}

func (t *Task) setStatus(s TaskStatus, errMsg string) {
	t.mu.Lock()
	t.Status = s
	t.Error = errMsg
	if s == StatusCompleted || s == StatusFailed || s == StatusCancelled {
		now := time.Now()
		t.FinishedAt = &now
	}
	t.mu.Unlock()
	t.broadcast()
}

// tryStart 原子地把排队中的任务切换为运行中，避免与取消操作竞态。
func (t *Task) tryStart() bool {
	t.mu.Lock()
	if t.Status != StatusQueued {
		t.mu.Unlock()
		return false
	}
	t.Status = StatusRunning
	t.Error = ""
	t.mu.Unlock()
	t.broadcast()
	return true
}

func (t *Task) setTotal(total int) {
	t.mu.Lock()
	t.Total = total
	t.mu.Unlock()
}

func (t *Task) updateProgress(p DownloadProgress) {
	t.mu.Lock()
	if p.Total > 0 {
		t.Total = p.Total
	}
	t.Completed = p.Completed
	t.Bytes = p.Bytes
	t.Speed = p.Speed
	t.mu.Unlock()
}

func (t *Task) setCancel(cancel context.CancelFunc) {
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
}

func (t *Task) cancelTask() {
	t.mu.RLock()
	cancel := t.cancel
	t.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (t *Task) reset() {
	t.mu.Lock()
	t.Status = StatusQueued
	t.Error = ""
	t.Completed = 0
	t.Bytes = 0
	t.Speed = 0
	t.FinishedAt = nil
	t.mu.Unlock()
	t.broadcast()
}

func (t *Task) subscribe() (chan ProgressEvent, func()) {
	ch := make(chan ProgressEvent, 8)

	t.mu.Lock()
	t.subscribers[ch] = struct{}{}
	t.mu.Unlock()

	select {
	case ch <- t.event():
	default:
	}

	return ch, func() {
		t.mu.Lock()
		delete(t.subscribers, ch)
		t.mu.Unlock()
		// 不关闭 channel：broadcast 可能已复制了该 channel 的引用，
		// 关闭后发送会 panic。删除后由 GC 回收即可。
	}
}

func (t *Task) broadcast() {
	ev := t.event()

	t.mu.RLock()
	subs := make([]chan ProgressEvent, 0, len(t.subscribers))
	for ch := range t.subscribers {
		subs = append(subs, ch)
	}
	t.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			// 客户端消费过慢时丢弃本次事件，下一次广播会补上最新快照。
		}
	}
}

func (t *Task) broadcastLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			t.broadcast()
		}
	}
}

// persistedTask 是任务记录落盘格式。
type persistedTask struct {
	ID         string          `json:"id"`
	URL        string          `json:"url"`
	Name       string          `json:"name"`
	Cmdline    string          `json:"cmdline"`
	Status     TaskStatus      `json:"status"`
	Total      int             `json:"total"`
	Completed  int32           `json:"completed"`
	Bytes      int64           `json:"bytes"`
	Error      string          `json:"error,omitempty"`
	Options    DownloadOptions `json:"options"`
	CreatedAt  time.Time       `json:"created_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// Manager 负责任务队列、状态机、持久化与并发控制。
type Manager struct {
	mu            sync.RWMutex
	tasks         map[string]*Task
	order         []string
	queue         chan *Task
	cfg           AppConfig
	configPath    string
	downloadDir   string
	tasksPath     string
	maxConcurrent int
	saveMu        sync.Mutex
}

// NewManager 创建任务管理器。
func NewManager(cfg AppConfig, configPath, downloadDir string, maxConcurrent int) *Manager {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Manager{
		tasks:         make(map[string]*Task),
		queue:         make(chan *Task, 1024),
		cfg:           cfg,
		configPath:    configPath,
		downloadDir:   downloadDir,
		tasksPath:     filepath.Join(filepath.Dir(configPath), "tasks.json"),
		maxConcurrent: maxConcurrent,
	}
}

// Start 加载历史任务并启动工作协程。
func (m *Manager) Start() {
	m.load()
	for i := 0; i < m.maxConcurrent; i++ {
		go m.worker()
	}
}

func (m *Manager) worker() {
	for task := range m.queue {
		if !m.exists(task.ID) {
			continue
		}
		if task.snapshot().Status != StatusQueued {
			continue
		}
		m.runTask(task)
	}
}

func (m *Manager) exists(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.tasks[id]
	return ok
}

// DownloadDir 返回 Web 模式下的下载根目录。
func (m *Manager) DownloadDir() string {
	return m.downloadDir
}

// Config 返回当前生效的全局配置。
func (m *Manager) Config() AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// SetConfig 更新内存中的全局配置。
func (m *Manager) SetConfig(cfg AppConfig) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// List 返回按创建顺序排列的任务快照。
func (m *Manager) List() []TaskView {
	m.mu.RLock()
	ids := append([]string(nil), m.order...)
	m.mu.RUnlock()

	views := make([]TaskView, 0, len(ids))
	for _, id := range ids {
		m.mu.RLock()
		t := m.tasks[id]
		m.mu.RUnlock()
		if t != nil {
			views = append(views, t.snapshot())
		}
	}
	return views
}

// Get 按 ID 查找任务。
func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	return t, ok
}

// CreateTask 解析参数串、创建任务并入队。
func (m *Manager) CreateTask(cmdline string) (TaskView, error) {
	opts, err := ParseCommandLine(cmdline, m.Config())
	if err != nil {
		return TaskView{}, err
	}
	if err := ValidateURL(opts.URL); err != nil {
		return TaskView{}, err
	}

	// Web 模式下所有成品统一保存到 DOWNLOAD_DIR，忽略 -d。
	opts.OutDir = m.downloadDir
	if err := validateName(opts.OutName); err != nil {
		return TaskView{}, err
	}

	task := newTask(newTaskID(), cmdline, opts)

	m.mu.Lock()
	m.tasks[task.ID] = task
	m.order = append(m.order, task.ID)
	m.mu.Unlock()

	select {
	case m.queue <- task:
	default:
		m.mu.Lock()
		delete(m.tasks, task.ID)
		m.removeOrderLocked(task.ID)
		m.mu.Unlock()
		return TaskView{}, fmt.Errorf("任务队列已满，请稍后重试")
	}

	m.save()
	return task.snapshot(), nil
}

// Cancel 取消排队中或运行中的任务。
func (m *Manager) Cancel(id string) error {
	t, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}

	switch t.snapshot().Status {
	case StatusQueued:
		t.setStatus(StatusCancelled, "任务已取消")
		m.save()
		return nil
	case StatusRunning, StatusMerging:
		t.cancelTask()
		t.setStatus(StatusCancelled, "任务已取消")
		m.save()
		return nil
	default:
		return fmt.Errorf("任务已结束，无需取消")
	}
}

// Retry 重试失败或已取消的任务，已下载的碎片会被复用。
func (m *Manager) Retry(id string) error {
	t, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}

	status := t.snapshot().Status
	if status != StatusFailed && status != StatusCancelled {
		return fmt.Errorf("只有失败或已取消的任务可以重试")
	}

	t.reset()
	select {
	case m.queue <- t:
	default:
		return fmt.Errorf("任务队列已满，请稍后重试")
	}

	m.save()
	return nil
}

// Delete 删除任务记录，可选同时删除成品文件与临时碎片。
func (m *Manager) Delete(id string, deleteFile bool) error {
	t, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("任务不存在")
	}

	view := t.snapshot()
	if view.Status == StatusRunning || view.Status == StatusMerging {
		return fmt.Errorf("任务正在运行，请先取消")
	}

	if deleteFile {
		if err := validateName(view.Options.OutName); err == nil {
			os.Remove(filepath.Join(m.downloadDir, view.Options.OutName+".mp4"))
			os.RemoveAll(filepath.Join(m.downloadDir, view.Options.OutName+"_temp"))
		}
	}

	m.mu.Lock()
	delete(m.tasks, id)
	m.removeOrderLocked(id)
	m.mu.Unlock()

	m.save()
	return nil
}

func (m *Manager) removeOrderLocked(id string) {
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func (m *Manager) runTask(t *Task) {
	if !t.tryStart() {
		return
	}
	m.save()

	ctx, cancel := context.WithCancel(context.Background())
	t.setCancel(cancel)
	defer cancel()

	// 与取消操作竞态兜底：若启动瞬间已被取消，直接退出。
	if t.snapshot().Status != StatusRunning {
		return
	}

	stop := make(chan struct{})
	go t.broadcastLoop(stop)
	defer close(stop)

	view := t.snapshot()

	d, err := NewDownloader(view.Options, m.Config())
	if err != nil {
		t.setStatus(StatusFailed, err.Error())
		m.save()
		return
	}

	body, err := d.Fetch(ctx, view.Options.URL)
	if err != nil {
		m.failTask(t, ctx, fmt.Errorf("无法获取 M3U8: %w", err))
		return
	}

	baseURL, err := url.Parse(view.Options.URL)
	if err != nil {
		m.failTask(t, ctx, fmt.Errorf("URL 非法: %w", err))
		return
	}

	key := d.GetM3u8Key(ctx, baseURL, string(body))
	tsList := ParseTsList(baseURL, string(body))
	if len(tsList) == 0 {
		m.failTask(t, ctx, fmt.Errorf("未解析到视频碎片，请确认链接是包含 .ts 碎片的子 M3U8"))
		return
	}

	t.setTotal(len(tsList))
	t.broadcast()

	tempDir := filepath.Join(m.downloadDir, view.Options.OutName+"_temp")
	finalPath := filepath.Join(m.downloadDir, view.Options.OutName+".mp4")

	if err := os.MkdirAll(tempDir, 0755); err != nil {
		m.failTask(t, ctx, fmt.Errorf("创建临时目录失败: %w", err))
		return
	}

	if _, err := os.Stat(finalPath); err == nil {
		if view.Options.Force {
			os.Remove(finalPath)
		} else {
			m.failTask(t, ctx, fmt.Errorf("文件冲突: 已存在 %s.mp4，如需覆盖请在参数中加 -f", view.Options.OutName))
			return
		}
	}

	if err := d.DownloadAll(ctx, tsList, tempDir, key, t.updateProgress); err != nil {
		if ctx.Err() != nil {
			t.setStatus(StatusCancelled, "任务已取消")
			if !view.Options.KeepTs {
				os.RemoveAll(tempDir)
			}
			m.save()
			return
		}
		m.failTask(t, ctx, err)
		return
	}

	t.setStatus(StatusMerging, "")
	m.save()

	if err := MergeTs(tsList, tempDir, finalPath); err != nil {
		m.failTask(t, ctx, fmt.Errorf("合并失败: %w", err))
		return
	}

	if !view.Options.KeepTs {
		os.RemoveAll(tempDir)
	}

	t.updateProgress(DownloadProgress{
		Total:     len(tsList),
		Completed: int32(len(tsList)),
		Bytes:     t.snapshot().Bytes,
	})
	t.setStatus(StatusCompleted, "")
	m.save()
}

func (m *Manager) failTask(t *Task, ctx context.Context, err error) {
	if ctx.Err() != nil {
		t.setStatus(StatusCancelled, "任务已取消")
	} else {
		t.setStatus(StatusFailed, err.Error())
	}
	m.save()
}

// save 将任务记录原子写入 tasks.json。
func (m *Manager) save() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.mu.RLock()
	list := make([]persistedTask, 0, len(m.order))
	for _, id := range m.order {
		t := m.tasks[id]
		if t == nil {
			continue
		}
		v := t.snapshot()
		list = append(list, persistedTask{
			ID:         v.ID,
			URL:        v.URL,
			Name:       v.Name,
			Cmdline:    v.Cmdline,
			Status:     v.Status,
			Total:      v.Total,
			Completed:  v.Completed,
			Bytes:      v.Bytes,
			Error:      v.Error,
			Options:    v.Options,
			CreatedAt:  v.CreatedAt,
			FinishedAt: v.FinishedAt,
		})
	}
	m.mu.RUnlock()

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(m.tasksPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("⚠️ 创建任务记录目录失败: %v", err)
			return
		}
	}

	tmp := m.tasksPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("⚠️ 写入任务记录失败: %v", err)
		return
	}
	if err := os.Rename(tmp, m.tasksPath); err != nil {
		log.Printf("⚠️ 保存任务记录失败: %v", err)
	}
}

// load 读取历史任务，并把因服务重启而中断的任务标记为失败（可重试续传）。
func (m *Manager) load() {
	data, err := os.ReadFile(m.tasksPath)
	if err != nil {
		return
	}

	var list []persistedTask
	if err := json.Unmarshal(data, &list); err != nil {
		log.Printf("⚠️ 任务记录损坏，已忽略: %v", err)
		return
	}

	changed := false
	for _, p := range list {
		if p.ID == "" || p.Options.OutName == "" {
			continue
		}
		status := p.Status
		errMsg := p.Error
		finishedAt := p.FinishedAt
		if status == StatusRunning || status == StatusMerging || status == StatusQueued {
			status = StatusFailed
			errMsg = "服务重启导致任务中断，可点击重试继续下载"
			now := time.Now()
			finishedAt = &now
			changed = true
		}

		t := &Task{
			ID:          p.ID,
			URL:         p.URL,
			Name:        p.Name,
			Cmdline:     p.Cmdline,
			Status:      status,
			Total:       p.Total,
			Completed:   p.Completed,
			Bytes:       p.Bytes,
			Error:       errMsg,
			Options:     p.Options,
			CreatedAt:   p.CreatedAt,
			FinishedAt:  finishedAt,
			subscribers: make(map[chan ProgressEvent]struct{}),
		}
		if t.CreatedAt.IsZero() {
			t.CreatedAt = time.Now()
		}

		m.tasks[t.ID] = t
		m.order = append(m.order, t.ID)
	}

	if changed {
		m.save()
	}
}

func newTaskID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("t_%s_%d", time.Now().Format("20060102150405"), time.Now().UnixNano()%1000000)
	}
	return "t_" + time.Now().Format("20060102150405") + "_" + hex.EncodeToString(b)
}

// validateName 防止文件名中出现路径分隔符或越界路径。
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("文件名不能为空")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("文件名不合法")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("文件名不能包含路径分隔符")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("文件名不能包含 ..")
	}
	for _, r := range name {
		if r == 0 {
			return fmt.Errorf("文件名包含非法字符")
		}
	}
	return nil
}
