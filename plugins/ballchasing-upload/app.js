// ballchasing-upload — bundled plugin that auto-uploads saved replays
// to ballchasing.com.
//
// app.js is loaded by both dashboard.html and settings.html. The HTML
// sets window.__rlt_view to "dashboard" or "settings" before the
// script loads so we can wire each view's behavior here.

(function () {
  const view = window.__rlt_view;
  if (!view) {
    console.warn("[ballchasing-upload] missing window.__rlt_view; aborting");
    return;
  }

  // ── Settings storage ────────────────────────────────────────
  const SETTINGS_KEY = "settings";
  const DEFAULT_SETTINGS = { apiKey: "", visibility: "private", group: "" };

  async function loadSettings() {
    const raw = await RLT.store.get(SETTINGS_KEY);
    return Object.assign({}, DEFAULT_SETTINGS, raw || {});
  }
  async function saveSettings(s) {
    await RLT.store.set(SETTINGS_KEY, s);
  }

  // ── Queue storage ───────────────────────────────────────────
  const QUEUE_KEY = "queue";
  const QUEUE_CAP = 100;

  async function loadQueue() {
    const raw = await RLT.store.get(QUEUE_KEY);
    return Array.isArray(raw) ? raw : [];
  }

  async function saveQueue(q) {
    // Cap to QUEUE_CAP entries by savedAt ascending (oldest first
    // dropped). The dashboard shows the last 50 in descending order.
    const sorted = q.slice().sort((a, b) =>
      (a.savedAt || "").localeCompare(b.savedAt || ""));
    const trimmed = sorted.slice(Math.max(0, sorted.length - QUEUE_CAP));
    await RLT.store.set(QUEUE_KEY, trimmed);
  }

  // ── Settings view ───────────────────────────────────────────
  async function initSettingsView() {
    const apiKeyEl = document.getElementById("apiKey");
    const groupEl = document.getElementById("group");
    const visEls = document.querySelectorAll('input[name="visibility"]');
    const saveBtn = document.getElementById("save");
    const testBtn = document.getElementById("test");
    const testResult = document.getElementById("test-result");

    const current = await loadSettings();
    apiKeyEl.value = current.apiKey;
    groupEl.value = current.group;
    for (const el of visEls) {
      if (el.value === current.visibility) el.checked = true;
    }

    function readForm() {
      let visibility = "private";
      for (const el of visEls) {
        if (el.checked) { visibility = el.value; break; }
      }
      return {
        apiKey: apiKeyEl.value.trim(),
        visibility,
        group: groupEl.value.trim(),
      };
    }

    saveBtn.addEventListener("click", async () => {
      await saveSettings(readForm());
      testResult.textContent = "Saved.";
      testResult.className = "test-result ok";
    });

    testBtn.addEventListener("click", async () => {
      const s = readForm();
      if (!s.apiKey) {
        testResult.textContent = "Enter an API key first.";
        testResult.className = "test-result err";
        return;
      }
      testResult.textContent = "Testing...";
      testResult.className = "test-result";
      try {
        const r = await fetch("https://ballchasing.com/api/", {
          headers: { Authorization: s.apiKey },
        });
        if (r.status === 200) {
          const body = await r.json();
          testResult.textContent = "Connected as " + (body.name || "(unknown)");
          testResult.className = "test-result ok";
        } else if (r.status === 401) {
          testResult.textContent = "Invalid API key.";
          testResult.className = "test-result err";
        } else {
          testResult.textContent = "Unexpected response: " + r.status;
          testResult.className = "test-result err";
        }
      } catch (e) {
        testResult.textContent = "Network error: " + e.message;
        testResult.className = "test-result err";
      }
    });
  }

  // ── Dashboard view ──────────────────────────────────────────
  // The dashboard owns the upload pump. Settings does not subscribe
  // to _SavedReplay or run the uploader to avoid double-firing.

  let queue = [];
  let pumpRunning = false;
  let wakeTimer = 0;

  function nextDue() {
    const now = Date.now();
    const ready = queue
      .filter(e => (e.status === "pending" || e.status === "retrying")
                && (e.nextAttemptAt || 0) <= now);
    if (ready.length === 0) return null;
    ready.sort((a, b) => (a.savedAt || "").localeCompare(b.savedAt || ""));
    return ready[0];
  }

  function armNextWakeTimer() {
    if (wakeTimer) clearTimeout(wakeTimer);
    wakeTimer = 0;
    const future = queue
      .filter(e => e.status === "retrying" && (e.nextAttemptAt || 0) > Date.now())
      .map(e => e.nextAttemptAt)
      .sort((a, b) => a - b);
    if (future.length === 0) return;
    const delay = Math.max(0, future[0] - Date.now());
    wakeTimer = setTimeout(() => {
      wakeTimer = 0;
      pump();
    }, delay);
  }

  async function pump() {
    if (pumpRunning) return;
    pumpRunning = true;
    try {
      while (true) {
        const entry = nextDue();
        if (!entry) {
          armNextWakeTimer();
          return;
        }
        await runOne(entry);
      }
    } finally {
      pumpRunning = false;
    }
  }

  // backoffMs returns the delay to wait BEFORE the next attempt,
  // given the new attempts counter (post-increment).
  //
  //   attempts == 1 (i.e. first try just failed): wait 30s
  //   attempts == 2 (second try failed):          wait 2min
  //   attempts == 3 (third try failed):           no more retries (caller handles)
  function backoffMs(attempts) {
    if (attempts <= 1) return 30 * 1000;
    if (attempts === 2) return 2 * 60 * 1000;
    return 0;
  }

  async function runOne(entry) {
    entry.status = "uploading";
    entry.lastError = "";
    await persistAndRender();

    const settings = await loadSettings();
    if (!settings.apiKey) {
      entry.status = "pending";
      await persistAndRender();
      // Pump will idle until the user saves an API key and reloads
      // (next _SavedReplay or page refresh kicks it).
      return;
    }

    // Fetch bytes from the backend.
    let bytes;
    try {
      const r = await fetch("/api/replay-file?path=" + encodeURIComponent(entry.path));
      if (r.status === 503) {
        entry.status = "failed_permanent";
        entry.lastError = "replay watcher inactive";
        await persistAndRender();
        return;
      }
      if (r.status === 404) {
        entry.status = "failed_permanent";
        entry.lastError = "replay file missing";
        await persistAndRender();
        return;
      }
      if (r.status === 413) {
        entry.status = "failed_permanent";
        entry.lastError = "replay file too large";
        await persistAndRender();
        return;
      }
      if (!r.ok) {
        entry.status = "failed_permanent";
        entry.lastError = "backend error: " + r.status;
        await persistAndRender();
        return;
      }
      bytes = await r.arrayBuffer();
    } catch (e) {
      entry.attempts = (entry.attempts || 0) + 1;
      if (entry.attempts >= 3) {
        entry.status = "failed_permanent";
        entry.lastError = "network error: " + e.message;
      } else {
        entry.status = "retrying";
        entry.lastError = "network error: " + e.message;
        entry.nextAttemptAt = Date.now() + backoffMs(entry.attempts);
      }
      await persistAndRender();
      return;
    }

    // Upload to ballchasing.
    const fd = new FormData();
    fd.append("file", new Blob([bytes]), entry.fileName);

    const params = new URLSearchParams({ visibility: settings.visibility });
    if (settings.group) params.set("group", settings.group);
    const uploadUrl = "https://ballchasing.com/api/v2/upload?" + params.toString();

    let resp;
    try {
      resp = await fetch(uploadUrl, {
        method: "POST",
        headers: { Authorization: settings.apiKey },
        body: fd,
      });
    } catch (e) {
      entry.attempts = (entry.attempts || 0) + 1;
      if (entry.attempts >= 3) {
        entry.status = "failed_permanent";
        entry.lastError = "network error: " + e.message;
      } else {
        entry.status = "retrying";
        entry.lastError = "network error: " + e.message;
        entry.nextAttemptAt = Date.now() + backoffMs(entry.attempts);
      }
      await persistAndRender();
      return;
    }

    if (resp.status === 201) {
      const body = await resp.json().catch(() => ({}));
      entry.status = "success";
      entry.ballchasingUrl = body.id ? ("https://ballchasing.com/replay/" + body.id) : "";
    } else if (resp.status === 409) {
      const body = await resp.json().catch(() => ({}));
      entry.status = "success_duplicate";
      entry.ballchasingUrl = body.id ? ("https://ballchasing.com/replay/" + body.id) : "";
    } else if (resp.status === 401) {
      entry.status = "failed_permanent";
      entry.lastError = "invalid API key";
    } else if (resp.status === 400) {
      entry.lastError = "bad request: " + (await resp.text().catch(() => ""));
      entry.status = "failed_permanent";
    } else if (resp.status === 429) {
      const retryAfter = parseInt(resp.headers.get("Retry-After") || "60", 10);
      entry.status = "retrying";
      entry.nextAttemptAt = Date.now() + (Number.isFinite(retryAfter) ? retryAfter : 60) * 1000;
      entry.lastError = "rate limited";
    } else if (resp.status >= 500) {
      entry.attempts = (entry.attempts || 0) + 1;
      if (entry.attempts >= 3) {
        entry.status = "failed_permanent";
        entry.lastError = "server error: " + resp.status;
      } else {
        entry.status = "retrying";
        entry.nextAttemptAt = Date.now() + backoffMs(entry.attempts);
        entry.lastError = "server error: " + resp.status;
      }
    } else {
      entry.status = "failed_permanent";
      entry.lastError = "unexpected status: " + resp.status;
    }
    await persistAndRender();
  }

  async function persistAndRender() {
    await saveQueue(queue);
    renderTable();
  }

  function renderTable() {
    const rows = document.getElementById("rows");
    if (!rows) return;
    const sorted = queue.slice().sort((a, b) =>
      (b.savedAt || "").localeCompare(a.savedAt || ""));
    const last50 = sorted.slice(0, 50);
    rows.innerHTML = "";
    for (const entry of last50) {
      const tr = document.createElement("tr");
      tr.appendChild(td(entry.fileName));
      tr.appendChild(statusCell(entry));
      tr.appendChild(td(formatTime(entry.savedAt)));
      tr.appendChild(actionCell(entry));
      rows.appendChild(tr);
    }
  }

  function td(text) {
    const el = document.createElement("td");
    el.textContent = text || "";
    return el;
  }

  function statusCell(entry) {
    const el = document.createElement("td");
    let label = entry.status;
    let cls = "status-pending";
    switch (entry.status) {
      case "success":
      case "success_duplicate":
        label = entry.status === "success_duplicate" ? "Uploaded (dup)" : "Uploaded";
        cls = "status-success";
        break;
      case "uploading":
        label = "Uploading...";
        cls = "status-uploading";
        break;
      case "retrying":
        label = "Retrying (" + (entry.attempts || 0) + "/3)";
        cls = "status-retrying";
        break;
      case "failed_permanent":
        label = "Failed: " + (entry.lastError || "unknown");
        cls = "status-fail";
        break;
      case "pending":
        label = "Pending";
        cls = "status-pending";
        break;
      case "cancelled":
        label = "Cancelled";
        cls = "status-pending";
        break;
    }
    el.textContent = label;
    el.className = cls;
    return el;
  }

  function actionCell(entry) {
    const el = document.createElement("td");
    if ((entry.status === "success" || entry.status === "success_duplicate") && entry.ballchasingUrl) {
      const a = document.createElement("a");
      a.href = entry.ballchasingUrl;
      a.textContent = "Open";
      a.target = "_blank";
      a.rel = "noopener";
      el.appendChild(a);
    } else if (entry.status === "pending" || entry.status === "retrying") {
      const btn = document.createElement("button");
      btn.textContent = "Cancel";
      btn.className = "secondary";
      btn.addEventListener("click", async () => {
        entry.status = "cancelled";
        await persistAndRender();
      });
      el.appendChild(btn);
    } else if (entry.status === "failed_permanent") {
      const retry = document.createElement("button");
      retry.textContent = "Retry";
      retry.addEventListener("click", async () => {
        entry.status = "pending";
        entry.attempts = 0;
        entry.nextAttemptAt = 0;
        entry.lastError = "";
        await persistAndRender();
        pump();
      });
      el.appendChild(retry);
      const remove = document.createElement("button");
      remove.textContent = "Remove";
      remove.className = "secondary";
      remove.style.marginLeft = "6px";
      remove.addEventListener("click", async () => {
        queue = queue.filter(e => e !== entry);
        await persistAndRender();
      });
      el.appendChild(remove);
    }
    return el;
  }

  function formatTime(iso) {
    if (!iso) return "";
    try {
      return new Date(iso).toLocaleTimeString();
    } catch (_) {
      return iso;
    }
  }

  async function refreshBanner() {
    const settings = await loadSettings();
    const banner = document.getElementById("banner");
    if (banner) banner.hidden = !!settings.apiKey;
  }

  async function initDashboardView() {
    queue = await loadQueue();
    renderTable();
    await refreshBanner();

    RLT.on("_SavedReplay", async (payload) => {
      // Defensive: skip if we already have a successful entry for
      // this exact path (a replay observed twice for any reason).
      const dup = queue.find(e => e.path === payload.path
        && (e.status === "success" || e.status === "success_duplicate"));
      if (dup) return;
      queue.push({
        matchGuid: payload.matchGuid,
        path: payload.path,
        fileName: payload.fileName,
        savedAt: payload.savedAt,
        status: "pending",
        attempts: 0,
        nextAttemptAt: 0,
        lastError: "",
        ballchasingUrl: "",
      });
      await persistAndRender();
      pump();
    });

    // Drain any pending/retrying entries from a prior session.
    pump();
  }

  // ── Dispatch ────────────────────────────────────────────────
  RLT.plugin.register({
    name: "ballchasing-upload",
    init() {
      if (view === "settings") {
        initSettingsView();
      } else if (view === "dashboard") {
        initDashboardView();
      }
    },
  });
})();
