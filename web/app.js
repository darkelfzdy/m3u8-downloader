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
    selected: new Set(),
    token: "",
    config: null,
    filter: "all",
    rendered: new Set(),
  };

  const isActive = (task) =>
    task.status === "queued" ||
    task.status === "running" ||
    task.status === "merging";

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

  function pad(value, width) {
    return String(value).padStart(width || 2, "0");
  }

  function toast(msg, type) {
    const box = $("#toasts");
    if (!box) return;
    const el = document.createElement("div");
    el.className = "toast " + (type || "info");
    el.textContent = msg;
    box.appendChild(el);
    requestAnimationFrame(function () {
      el.classList.add("show");
    });
    setTimeout(function () {
      el.classList.remove("show");
      setTimeout(function () {
        if (el.parentNode) el.parentNode.removeChild(el);
      }, 220);
    }, 3600);
  }

  function emptyState(code, text) {
    return (
      '<div class="empty"><span class="empty-code">' +
      escapeHtml(code) +
      '</span><span class="empty-text">' +
      escapeHtml(text) +
      '</span><span class="empty-glyph" aria-hidden="true">· · ·</span></div>'
    );
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

  function tickClock() {
    const clock = $("#clock");
    if (!clock) return;
    const d = new Date();
    clock.textContent =
      pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    const date = $("#clock-date");
    if (date) {
      date.textContent =
        d.getFullYear() + "." + pad(d.getMonth() + 1) + "." + pad(d.getDate());
    }
  }

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
          '"><span class="recent-dot" aria-hidden="true">↺</span><span class="recent-cmd">' +
          escapeHtml(item) +
          "</span></button>"
      )
      .join("");
  }

  /* ---------------- 任务 ---------------- */

  function sortedTasks() {
    return Array.from(state.tasks.values()).sort(
      (a, b) => new Date(b.created_at) - new Date(a.created_at)
    );
  }

  function matchingFilter(task) {
    switch (state.filter) {
      case "active":
        return isActive(task);
      case "done":
        return task.status === "completed";
      case "failed":
        return task.status === "failed" || task.status === "cancelled";
      default:
        return true;
    }
  }

  function visibleTasks() {
    return sortedTasks().filter(matchingFilter);
  }

  function updateStats(all) {
    const active = all.filter(isActive).length;
    const done = all.filter((t) => t.status === "completed").length;
    const failed = all.filter(
      (t) => t.status === "failed" || t.status === "cancelled"
    ).length;
    const queued = all.filter((t) => t.status === "queued").length;
    const streams = all.filter(
      (t) => t.status === "running" || t.status === "merging"
    ).length;

    const set = function (sel, value) {
      const el = $(sel);
      if (el) el.textContent = pad(value);
    };
    set("#stat-total", all.length);
    set("#stat-active", active);
    set("#stat-done", done);
    set("#stat-failed", failed);

    const queuedEl = $("#stat-queued");
    if (queuedEl) queuedEl.textContent = String(queued);
    const streamsEl = $("#stat-streams");
    if (streamsEl) streamsEl.textContent = String(streams);
  }

  function updateFilterCounts(all) {
    const counts = {
      all: all.length,
      active: all.filter(isActive).length,
      done: all.filter((t) => t.status === "completed").length,
      failed: all.filter(
        (t) => t.status === "failed" || t.status === "cancelled"
      ).length,
    };

    const filters = document.querySelectorAll("#task-filters [data-filter]");
    Array.prototype.forEach.call(filters, function (btn) {
      const key = btn.getAttribute("data-filter");
      const counter = btn.querySelector(".f-count");
      if (counter) counter.textContent = String(counts[key] || 0);
      btn.classList.toggle("active", key === state.filter);
    });
  }

  function setFilter(filter) {
    if (state.filter === filter) return;
    state.filter = filter;
    state.selected.clear();
    renderTasks();
  }

  function renderTasks() {
    const container = $("#tasks");
    const all = sortedTasks();
    const list = all.filter(matchingFilter);

    updateStats(all);
    updateFilterCounts(all);

    if (!all.length) {
      container.innerHTML = emptyState(
        "NO SIGNAL",
        "还没有任务，在上方输入地址开始"
      );
      state.rendered.clear();
      syncSelection([]);
      return;
    }

    if (!list.length) {
      container.innerHTML = emptyState("NO MATCH", "当前筛选下没有任务");
      syncSelection([]);
      return;
    }

    const order = new Map();
    all.forEach(function (task, i) {
      order.set(task.id, i);
    });

    container.innerHTML = list
      .map(function (task) {
        return renderTask(task, order.get(task.id));
      })
      .join("");
    syncSelection(list);
  }

  // syncSelection 清理已不存在的选中项，并同步全选框与批量操作栏。
  function syncSelection(list) {
    const ids = new Set(list.map((t) => t.id));
    Array.from(state.selected).forEach((id) => {
      if (!ids.has(id)) state.selected.delete(id);
    });

    const all = $("#select-all");
    const selectedCount = list.filter((t) => state.selected.has(t.id)).length;
    all.disabled = list.length === 0;
    all.checked = list.length > 0 && selectedCount === list.length;
    all.indeterminate = selectedCount > 0 && selectedCount < list.length;

    updateBatchBar(list);
  }

  function updateBatchBar(list) {
    list = list || sortedTasks();
    const selected = list.filter((t) => state.selected.has(t.id));

    $("#batch-bar").classList.toggle("hidden", selected.length === 0);
    $("#batch-count").textContent = "已选 " + selected.length + " 项";

    const cancelable = selected.filter(
      (t) =>
        t.status === "queued" ||
        t.status === "running" ||
        t.status === "merging"
    ).length;
    const retryable = selected.filter(
      (t) => t.status === "failed" || t.status === "cancelled"
    ).length;
    const deletable = selected.filter(
      (t) => t.status !== "running" && t.status !== "merging"
    ).length;

    setBatchEnabled("#btn-batch-cancel", cancelable, "所选任务中没有可取消的任务");
    setBatchEnabled("#btn-batch-retry", retryable, "所选任务中没有可重试的任务");
    setBatchEnabled("#btn-batch-delete", deletable, "所选任务中没有可删除的任务");
    setBatchEnabled(
      "#btn-batch-delete-file",
      deletable,
      "所选任务中没有可删除的任务"
    );
  }

  function setBatchEnabled(sel, count, hint) {
    const btn = $(sel);
    btn.disabled = count === 0;
    btn.title = count === 0 ? hint : "";
  }

  function renderTask(task, index) {
    const total = task.total || 0;
    const completed = task.completed || 0;
    let percent = total > 0 ? Math.min(100, (completed / total) * 100) : 0;
    if (percent <= 0 && task.status === "completed") percent = 100;

    const isRunning =
      task.status === "running" || task.status === "merging";
    const canRetry =
      task.status === "failed" || task.status === "cancelled";
    const selected = state.selected.has(task.id);

    const fresh = !state.rendered.has(task.id);
    state.rendered.add(task.id);

    const actions = [];
    if (isActive(task)) {
      actions.push(
        '<button class="btn btn-mini danger" data-action="cancel" data-id="' +
          escapeHtml(task.id) +
          '">取消</button>'
      );
    }
    if (canRetry) {
      actions.push(
        '<button class="btn btn-mini" data-action="retry" data-id="' +
          escapeHtml(task.id) +
          '">重试</button>'
      );
    }
    if (!isRunning) {
      actions.push(
        '<button class="btn btn-mini" data-action="delete" data-id="' +
          escapeHtml(task.id) +
          '">删除</button>'
      );
    }

    const speed =
      task.status === "running"
        ? (task.speed || 0).toFixed(2) + " MB/s"
        : "—";

    const percentText =
      percent >= 100 ? "100" : percent.toFixed(percent >= 10 ? 1 : 2);

    return (
      '<article class="task ' +
      escapeHtml(task.status) +
      (selected ? " selected" : "") +
      (fresh ? " is-new" : "") +
      '"' +
      (fresh
        ? ' style="animation-delay:' + Math.min(index * 40, 240) + 'ms"'
        : "") +
      ">" +
      '<label class="task-check">' +
      '<input type="checkbox" data-select="' +
      escapeHtml(task.id) +
      '"' +
      (selected ? " checked" : "") +
      ' aria-label="选择任务">' +
      "</label>" +
      '<div class="task-main">' +
      '<div class="task-line">' +
      '<span class="task-idx">' +
      pad(index + 1, 3) +
      "</span>" +
      '<span class="task-name" title="' +
      escapeHtml((task.name || task.id) + ".mp4") +
      '">' +
      escapeHtml(task.name || task.id) +
      ".mp4</span>" +
      '<span class="badge ' +
      escapeHtml(task.status) +
      '">' +
      escapeHtml(statusLabel(task)) +
      "</span>" +
      "</div>" +
      '<div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' +
      percent.toFixed(0) +
      '"><div class="bar-fill" style="width:' +
      percent.toFixed(1) +
      '%"></div></div>' +
      '<div class="task-meta">' +
      '<span class="meta-item"><b>' +
      percentText +
      "%</b><span>进度</span></span>" +
      '<span class="meta-item"><b>' +
      completed +
      " / " +
      total +
      "</b><span>碎片</span></span>" +
      '<span class="meta-item"><b>' +
      speed +
      "</b><span>速度</span></span>" +
      '<span class="meta-item"><b>' +
      formatSize(task.bytes || 0) +
      "</b><span>已下载</span></span>" +
      '<span class="meta-item task-time">' +
      formatTime(task.created_at) +
      "</span>" +
      "</div>" +
      (task.error
        ? '<div class="task-error">' + escapeHtml(task.error) + "</div>"
        : "") +
      "</div>" +
      (actions.length
        ? '<div class="task-actions">' + actions.join("") + "</div>"
        : '<div class="task-actions"></div>') +
      "</article>"
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
          state.rendered.delete(id);
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
      state.filter = "all";
      renderTasks();
      subscribe(task.id);
      $("#cmdline").value = "";
      toast("任务已加入队列：" + (task.name || task.id), "success");
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
        toast(e.message, "error");
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
        toast(e.message, "error");
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
        state.rendered.delete(id);
        state.selected.delete(id);
        closeSSE(id);
        renderTasks();
      } catch (e) {
        toast(e.message, "error");
      }
    }
  }

  function taskName(id) {
    const task = state.tasks.get(id);
    return task ? task.name || id : id;
  }

  async function batchAction(action) {
    const selected = visibleTasks().filter((t) => state.selected.has(t.id));
    if (!selected.length) return;

    let ids = [];
    let skipped = 0;
    let deleteFile = false;
    let tip = "";

    if (action === "cancel") {
      ids = selected
        .filter(
          (t) =>
            t.status === "queued" ||
            t.status === "running" ||
            t.status === "merging"
        )
        .map((t) => t.id);
      if (!ids.length) {
        toast("所选任务中没有可取消的任务", "warn");
        return;
      }
      skipped = selected.length - ids.length;
      tip =
        "确定取消选中的 " +
        ids.length +
        " 个任务？\n取消后临时碎片目录会被清理。";
    } else if (action === "retry") {
      ids = selected
        .filter((t) => t.status === "failed" || t.status === "cancelled")
        .map((t) => t.id);
      if (!ids.length) {
        toast("所选任务中没有可重试的任务", "warn");
        return;
      }
      skipped = selected.length - ids.length;
      tip = "确定重试选中的 " + ids.length + " 个任务？";
    } else if (action === "delete" || action === "delete-file") {
      deleteFile = action === "delete-file";
      ids = selected
        .filter((t) => t.status !== "running" && t.status !== "merging")
        .map((t) => t.id);
      if (!ids.length) {
        toast("所选任务中有正在运行或合并的任务，请先取消后再删除", "warn");
        return;
      }
      skipped = selected.length - ids.length;
      tip = deleteFile
        ? "确定删除选中的 " +
          ids.length +
          " 个任务记录，并同时删除成品文件和临时碎片？\n删除文件后无法恢复。"
        : "确定删除选中的 " + ids.length + " 个任务记录？";
    } else {
      return;
    }

    if (skipped > 0) {
      tip += "\n另有 " + skipped + " 个任务状态不满足，将被跳过。";
    }
    if (!confirm(tip)) return;

    const apiAction = action === "delete-file" ? "delete" : action;

    try {
      const data = await api("/api/tasks/batch", {
        method: "POST",
        body: JSON.stringify({
          action: apiAction,
          ids: ids,
          delete_file: deleteFile,
        }),
      });
      const results = (data && data.results) || [];
      const okIds = results.filter((r) => r.ok).map((r) => r.id);
      const failed = results.filter((r) => !r.ok);

      if (apiAction === "delete") {
        okIds.forEach(function (id) {
          state.selected.delete(id);
          closeSSE(id);
        });
      }

      await loadTasks();

      if (failed.length) {
        toast(
          "操作完成，但有 " +
            failed.length +
            " 项失败：\n" +
            failed
              .map((r) => taskName(r.id) + "：" + (r.error || "未知错误"))
              .join("\n"),
          "error"
        );
      } else {
        toast("批量操作完成，共 " + okIds.length + " 项", "success");
      }
    } catch (e) {
      toast(e.message, "error");
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

    $("#tasks").addEventListener("change", function (e) {
      const box = e.target.closest("[data-select]");
      if (!box) return;
      const id = box.getAttribute("data-select");
      if (box.checked) {
        state.selected.add(id);
      } else {
        state.selected.delete(id);
      }
      const card = box.closest(".task");
      if (card) card.classList.toggle("selected", box.checked);
      updateBatchBar();
    });

    $("#task-filters").addEventListener("click", function (e) {
      const btn = e.target.closest("[data-filter]");
      if (!btn) return;
      setFilter(btn.getAttribute("data-filter"));
    });

    $("#select-all").addEventListener("change", function (e) {
      const list = visibleTasks();
      if (e.target.checked) {
        list.forEach((t) => state.selected.add(t.id));
      } else {
        list.forEach((t) => state.selected.delete(t.id));
      }
      renderTasks();
    });

    $("#batch-bar").addEventListener("click", function (e) {
      const btn = e.target.closest("[data-batch]");
      if (!btn || btn.disabled) return;
      batchAction(btn.getAttribute("data-batch"));
    });
  }

  /* ---------------- 启动 ---------------- */

  function init() {
    state.token = localStorage.getItem("m3u8_token") || "";
    loadRecent();
    bind();
    tickClock();
    checkHealth();
    loadVersion();
    loadConfig();
    loadTasks();

    setInterval(tickClock, 1000);
    setInterval(checkHealth, 30000);
    setInterval(loadTasks, 5000);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
