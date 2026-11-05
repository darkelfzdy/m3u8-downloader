/* M3U8 Downloader Web UI */
(function () {
  "use strict";

  const $ = (sel) => document.querySelector(sel);

  const STATUS_TEXT = {
    queued: "排队中",
    running: "下载中",
    merging: "合并中",
    completed: "已完成",
    failed: "失败",
    cancelled: "已取消",
  };

  const state = {
    tasks: new Map(),
    sse: new Map(),
    recent: [],
    token: "",
    config: null,
  };

  /* ---------------- 工具函数 ---------------- */

  function escapeHtml(str) {
    return String(str == null ? "" : str)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function formatSize(bytes) {
    if (!bytes || bytes <= 0) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let v = bytes;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + " " + units[i];
  }

  function formatTime(value) {
    if (!value) return "-";
    const d = new Date(value);
    if (isNaN(d.getTime())) return "-";
    const p = (n) => String(n).padStart(2, "0");
    return (
      d.getFullYear() +
      "-" +
      p(d.getMonth() + 1) +
      "-" +
      p(d.getDate()) +
      " " +
      p(d.getHours()) +
      ":" +
      p(d.getMinutes()) +
      ":" +
      p(d.getSeconds())
    );
  }

  function statusLabel(task) {
    if (
      task.status === "failed" &&
      task.error &&
      task.error.indexOf("中断") >= 0
    ) {
      return "已中断";
    }
    return STATUS_TEXT[task.status] || task.status;
  }

  function withToken(url) {
    if (!state.token) return url;
    const sep = url.indexOf("?") >= 0 ? "&" : "?";
    return url + sep + "token=" + encodeURIComponent(state.token);
  }

  async function api(path, options) {
    options = options || {};
    const headers = Object.assign({}, options.headers || {});
    if (options.body && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    if (state.token) {
      headers["Authorization"] = "Bearer " + state.token;
    }

    const res = await fetch(path, Object.assign({}, options, { headers }));
    let data = null;
    const text = await res.text();
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (e) {
        data = { raw: text };
      }
    }

    if (res.status === 401) {
      openSettings();
      throw new Error("访问令牌无效，请在设置中填写 WEB_TOKEN");
    }
    if (!res.ok) {
      throw new Error((data && data.error) || "请求失败: HTTP " + res.status);
    }
    return data;
  }

  /* ---------------- 健康状态与版本 ---------------- */

  async function checkHealth() {
    const dot = $("#health-dot");
    const text = $("#health-text");
    try {
      const res = await fetch("/healthz", { cache: "no-store" });
      if (!res.ok) throw new Error("bad status");
      dot.className = "dot online";
      text.textContent = "服务正常";
    } catch (e) {
      dot.className = "dot offline";
      text.textContent = "服务异常";
    }
  }

  async function loadVersion() {
    try {
      const data = await fetch("/api/info").then((r) => r.json());
      $("#version").textContent = "版本 " + (data.version || "dev");
    } catch (e) {
      /* ignore */
    }
  }

  /* ---------------- 最近提交 ---------------- */

  function loadRecent() {
    try {
      state.recent = JSON.parse(localStorage.getItem("m3u8_recent") || "[]");
    } catch (e) {
      state.recent = [];
    }
    renderRecent();
  }

  function remember(cmdline) {
    state.recent = [cmdline]
      .concat(state.recent.filter((x) => x !== cmdline))
      .slice(0, 10);
    localStorage.setItem("m3u8_recent", JSON.stringify(state.recent));
    renderRecent();
  }

  function renderRecent() {
    const wrap = $("#recent-wrap");
    const box = $("#recent");
    if (!state.recent.length) {
      wrap.classList.add("hidden");
      box.innerHTML = "";
      return;
    }
    wrap.classList.remove("hidden");
    box.innerHTML = state.recent
      .map(
        (item) =>
          '<button class="recent-item" data-recent="' +
          escapeHtml(item) +
          '" title="' +
          escapeHtml(item) +
          '">' +
          escapeHtml(item) +
          "</button>"
      )
      .join("");
  }

  /* ---------------- 任务 ---------------- */

  function renderTasks() {
    const container = $("#tasks");
    const list = Array.from(state.tasks.values()).sort(
      (a, b) => new Date(b.created_at) - new Date(a.created_at)
    );

    if (!list.length) {
      container.innerHTML = '<div class="empty">暂无任务</div>';
      return;
    }

    container.innerHTML = list.map(renderTask).join("");
  }

  function renderTask(task) {
    const total = task.total || 0;
    const completed = task.completed || 0;
    const percent = total > 0 ? Math.min(100, (completed / total) * 100) : 0;

    const isRunning =
      task.status === "running" || task.status === "merging";
    const canRetry =
      task.status === "failed" || task.status === "cancelled";

    const actions = [];
    if (isRunning || task.status === "queued") {
      actions.push(
        '<button class="danger" data-action="cancel" data-id="' +
          escapeHtml(task.id) +
          '">取消</button>'
      );
    }
    if (canRetry) {
      actions.push(
        '<button data-action="retry" data-id="' +
          escapeHtml(task.id) +
          '">重试</button>'
      );
    }
    if (!isRunning) {
      actions.push(
        '<button data-action="delete" data-id="' +
          escapeHtml(task.id) +
          '">删除</button>'
      );
    }

    const speed =
      task.status === "running" ? (task.speed || 0).toFixed(2) + " MB/s" : "-";

    return (
      '<div class="task ' +
      escapeHtml(task.status) +
      '">' +
      '<div class="task-head">' +
      '<span class="task-name">' +
      escapeHtml(task.name || task.id) +
      ".mp4</span>" +
      '<span class="badge ' +
      escapeHtml(task.status) +
      '">' +
      escapeHtml(statusLabel(task)) +
      "</span>" +
      "</div>" +
      '<div class="bar"><div style="width:' +
      percent.toFixed(1) +
      '%"></div></div>' +
      '<div class="meta">' +
      "<span>进度 " +
      completed +
      " / " +
      total +
      "（" +
      percent.toFixed(1) +
      "%）</span>" +
      "<span>速度 " +
      speed +
      "</span>" +
      "<span>已下载 " +
      formatSize(task.bytes || 0) +
      "</span>" +
      "<span>创建于 " +
      formatTime(task.created_at) +
      "</span>" +
      "</div>" +
      (task.error
        ? '<div class="task-error">' + escapeHtml(task.error) + "</div>"
        : "") +
      (actions.length
        ? '<div class="actions">' + actions.join("") + "</div>"
        : "") +
      "</div>"
    );
  }

  function upsertTask(task) {
    state.tasks.set(task.id, Object.assign({}, state.tasks.get(task.id), task));
  }

  function closeSSE(id) {
    const es = state.sse.get(id);
    if (es) {
      es.close();
      state.sse.delete(id);
    }
  }

  function subscribe(id) {
    if (state.sse.has(id)) return;
    const es = new EventSource(withToken("/api/tasks/" + encodeURIComponent(id) + "/events"));
    es.addEventListener("progress", function (ev) {
      try {
        const task = JSON.parse(ev.data);
        upsertTask(task);
        renderTasks();
        if (
          task.status === "completed" ||
          task.status === "failed" ||
          task.status === "cancelled"
        ) {
          closeSSE(id);
          if (task.status === "completed") loadFiles();
        }
      } catch (e) {
        /* ignore */
      }
    });
    es.onerror = function () {
      /* EventSource 会自动重连 */
    };
    state.sse.set(id, es);
  }

  async function loadTasks() {
    try {
      const data = await api("/api/tasks");
      const tasks = (data && data.tasks) || [];
      const seen = new Set();

      tasks.forEach(function (t) {
        seen.add(t.id);
        upsertTask(t);
      });

      Array.from(state.tasks.keys()).forEach(function (id) {
        if (!seen.has(id)) {
          state.tasks.delete(id);
          closeSSE(id);
        }
      });

      tasks.forEach(function (t) {
        if (
          t.status === "queued" ||
          t.status === "running" ||
          t.status === "merging"
        ) {
          subscribe(t.id);
        }
      });

      renderTasks();
    } catch (e) {
      showError(e.message);
    }
  }

  async function createTask() {
    const btn = $("#btn-create");
    const cmdline = $("#cmdline").value.trim();
    clearError();
    $("#probe-result").textContent = "";

    if (!cmdline) {
      showError("请输入下载参数，或直接粘贴 M3U8 地址");
      return;
    }

    btn.disabled = true;
    try {
      const task = await api("/api/tasks", {
        method: "POST",
        body: JSON.stringify({ cmdline: cmdline }),
      });
      remember(cmdline);
      upsertTask(task);
      renderTasks();
      subscribe(task.id);
      $("#cmdline").value = "";
      loadTasks();
    } catch (e) {
      showError(e.message);
    } finally {
      btn.disabled = false;
    }
  }

  async function probe() {
    const btn = $("#btn-probe");
    const cmdline = $("#cmdline").value.trim();
    clearError();
    $("#probe-result").textContent = "";

    if (!cmdline) {
      showError("请输入下载参数，或直接粘贴 M3U8 地址");
      return;
    }

    btn.disabled = true;
    $("#probe-result").textContent = "解析中…";
    try {
      const data = await api("/api/probe", {
        method: "POST",
        body: JSON.stringify({ cmdline: cmdline }),
      });
      $("#probe-result").textContent =
        "共 " +
        data.total +
        " 个碎片 · " +
        (data.encrypted ? "AES-128 加密" : "未加密") +
        " · 保存为 " +
        data.name +
        ".mp4";
    } catch (e) {
      $("#probe-result").textContent = "";
      showError(e.message);
    } finally {
      btn.disabled = false;
    }
  }

  async function taskAction(action, id) {
    if (action === "cancel") {
      if (!confirm("确定取消该任务？临时碎片目录会被清理。")) return;
      try {
        await api("/api/tasks/" + encodeURIComponent(id) + "/cancel", {
          method: "POST",
        });
        loadTasks();
      } catch (e) {
        alert(e.message);
      }
      return;
    }

    if (action === "retry") {
      try {
        await api("/api/tasks/" + encodeURIComponent(id) + "/retry", {
          method: "POST",
        });
        loadTasks();
      } catch (e) {
        alert(e.message);
      }
      return;
    }

    if (action === "delete") {
      if (!confirm("确定删除该任务记录？")) return;
      const deleteFile = confirm(
        "是否同时删除已下载的成品文件？\n确定 = 删除文件，取消 = 仅删除记录"
      );
      try {
        await api(
          "/api/tasks/" +
            encodeURIComponent(id) +
            "?delete_file=" +
            (deleteFile ? "true" : "false"),
          { method: "DELETE" }
        );
        state.tasks.delete(id);
        closeSSE(id);
        renderTasks();
        if (deleteFile) loadFiles();
      } catch (e) {
        alert(e.message);
      }
    }
  }

  /* ---------------- 文件 ---------------- */

  async function loadFiles() {
    try {
      const data = await api("/api/files");
      const files = (data && data.files) || [];
      const box = $("#files");

      if (!files.length) {
        box.innerHTML = '<div class="empty">暂无文件</div>';
        return;
      }

      box.innerHTML = files
        .map(function (f) {
          return (
            '<div class="file">' +
            '<span class="file-name">' +
            escapeHtml(f.name) +
            "</span>" +
            '<span class="file-meta">' +
            formatSize(f.size) +
            " · " +
            formatTime(f.mod_time) +
            "</span>" +
            '<a href="' +
            escapeHtml(withToken("/api/files/" + encodeURIComponent(f.name))) +
            '" download>下载</a>' +
            "</div>"
          );
        })
        .join("");
    } catch (e) {
      /* ignore */
    }
  }

  /* ---------------- 设置 ---------------- */

  function openSettings() {
    $("#settings").classList.remove("hidden");
    $("#mask").classList.remove("hidden");
    loadConfig();
  }

  function closeSettings() {
    $("#settings").classList.add("hidden");
    $("#mask").classList.add("hidden");
  }

  async function loadConfig() {
    try {
      const cfg = await api("/api/config");
      state.config = cfg;
      $("#cfg-threads").value = cfg.default_threads || 8;
      $("#cfg-timeout").value = cfg.timeout_sec || 60;
      $("#cfg-retries").value = cfg.max_retries || 0;
      $("#cfg-proxy").value = cfg.proxy || "";
      $("#cfg-insecure").checked = !!cfg.insecure;
      $("#cfg-uas").value = (cfg.user_agents || []).join("\n");
      $("#cfg-headers").value = JSON.stringify(
        cfg.advanced_headers || [],
        null,
        2
      );
      $("#cfg-token").value = state.token;
    } catch (e) {
      $("#config-msg").textContent = e.message;
    }
  }

  async function saveConfig() {
    const msg = $("#config-msg");
    msg.textContent = "";

    state.token = $("#cfg-token").value.trim();
    localStorage.setItem("m3u8_token", state.token);

    let headers;
    try {
      headers = JSON.parse($("#cfg-headers").value || "[]");
    } catch (e) {
      msg.textContent = "高级请求头不是合法的 JSON";
      return;
    }
    if (!Array.isArray(headers)) {
      msg.textContent = "高级请求头必须是 JSON 数组";
      return;
    }

    const uas = $("#cfg-uas")
      .value.split("\n")
      .map((x) => x.trim())
      .filter(Boolean);

    const cfg = {
      default_threads: parseInt($("#cfg-threads").value, 10) || 8,
      timeout_sec: parseInt($("#cfg-timeout").value, 10) || 60,
      max_retries: parseInt($("#cfg-retries").value, 10) || 0,
      proxy: $("#cfg-proxy").value.trim(),
      insecure: $("#cfg-insecure").checked,
      default_dir: (state.config && state.config.default_dir) || "",
      user_agents: uas,
      advanced_headers: headers,
    };

    const btn = $("#btn-save-config");
    btn.disabled = true;
    try {
      state.config = await api("/api/config", {
        method: "PUT",
        body: JSON.stringify(cfg),
      });
      msg.textContent = "已保存";
      setTimeout(function () {
        msg.textContent = "";
      }, 2000);
    } catch (e) {
      msg.textContent = e.message;
    } finally {
      btn.disabled = false;
    }
  }

  /* ---------------- 错误提示 ---------------- */

  function showError(msg) {
    const box = $("#create-error");
    box.textContent = msg;
    box.classList.remove("hidden");
  }

  function clearError() {
    const box = $("#create-error");
    box.textContent = "";
    box.classList.add("hidden");
  }

  /* ---------------- 事件绑定 ---------------- */

  function bind() {
    $("#btn-create").addEventListener("click", createTask);
    $("#btn-probe").addEventListener("click", probe);
    $("#btn-refresh").addEventListener("click", loadTasks);
    $("#btn-files-refresh").addEventListener("click", loadFiles);
    $("#btn-settings").addEventListener("click", openSettings);
    $("#btn-settings-close").addEventListener("click", closeSettings);
    $("#mask").addEventListener("click", closeSettings);
    $("#btn-save-config").addEventListener("click", saveConfig);

    $("#cmdline").addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault();
        createTask();
      }
    });

    $("#recent").addEventListener("click", function (e) {
      const btn = e.target.closest("[data-recent]");
      if (!btn) return;
      $("#cmdline").value = btn.getAttribute("data-recent");
      $("#cmdline").focus();
    });

    $("#tasks").addEventListener("click", function (e) {
      const btn = e.target.closest("[data-action]");
      if (!btn) return;
      taskAction(btn.getAttribute("data-action"), btn.getAttribute("data-id"));
    });
  }

  /* ---------------- 启动 ---------------- */

  function init() {
    state.token = localStorage.getItem("m3u8_token") || "";
    loadRecent();
    bind();
    checkHealth();
    loadVersion();
    loadConfig();
    loadTasks();
    loadFiles();

    setInterval(checkHealth, 30000);
    setInterval(loadTasks, 5000);
    setInterval(loadFiles, 15000);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
