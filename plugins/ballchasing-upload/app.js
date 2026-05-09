// ballchasing-upload — bundled plugin that auto-uploads saved replays
// to ballchasing.com.
//
// Two views:
//
//   - settings.html: API key + visibility + group + test connection.
//   - background.html: loaded as a hidden iframe by the launcher. Owns
//     the upload pump, subscribes to _SavedReplay, persists queue
//     state. Runs for as long as the launcher is open and the plugin
//     is enabled — independent of any visible UI tab.
//
// The HTML sets window.__rlt_view before this script loads.

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
    // dropped). Keeps the persisted state bounded across long
    // sessions.
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
        // Routed through the toolkit's /api/plugin-fetch proxy because
        // ballchasing.com doesn't return CORS headers; a direct fetch
        // from the iframe would fail the preflight.
        const proxied = "/api/plugin-fetch/ballchasing-upload?url="
          + encodeURIComponent("https://ballchasing.com/api/");
        const r = await fetch(proxied, {
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

  // ── Background pump ─────────────────────────────────────────
  // Owns the upload queue. Subscribes to _SavedReplay, fetches replay
  // bytes from the backend, POSTs them to ballchasing via the proxy,
  // updates persisted queue state. No UI, no notifications — the
  // user's only signal is "did the replay show up on ballchasing.com".

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

  // backoffMs: 30s → 2min → permanent fail.
  function backoffMs(attempts) {
    if (attempts <= 1) return 30 * 1000;
    if (attempts === 2) return 2 * 60 * 1000;
    return 0;
  }

  async function runOne(entry) {
    entry.status = "uploading";
    entry.lastError = "";
    await saveQueue(queue);

    const settings = await loadSettings();
    if (!settings.apiKey) {
      // Park the entry; next _SavedReplay or settings save will kick
      // the pump again and we'll re-check.
      entry.status = "pending";
      await saveQueue(queue);
      return;
    }

    // Fetch bytes from the backend (path-traversal/size guards in
    // /api/replay-file).
    let bytes;
    try {
      const r = await fetch("/api/replay-file?path=" + encodeURIComponent(entry.path));
      if (r.status === 503 || r.status === 404 || r.status === 413) {
        entry.status = "failed_permanent";
        entry.lastError = "backend " + r.status;
        await saveQueue(queue);
        return;
      }
      if (!r.ok) {
        entry.status = "failed_permanent";
        entry.lastError = "backend error: " + r.status;
        await saveQueue(queue);
        return;
      }
      bytes = await r.arrayBuffer();
    } catch (e) {
      entry.attempts = (entry.attempts || 0) + 1;
      if (entry.attempts >= 3) {
        entry.status = "failed_permanent";
        entry.lastError = "network: " + e.message;
      } else {
        entry.status = "retrying";
        entry.lastError = "network: " + e.message;
        entry.nextAttemptAt = Date.now() + backoffMs(entry.attempts);
      }
      await saveQueue(queue);
      return;
    }

    // POST through /api/plugin-fetch/ to ballchasing.
    const fd = new FormData();
    fd.append("file", new Blob([bytes]), entry.fileName);
    const params = new URLSearchParams({ visibility: settings.visibility });
    if (settings.group) params.set("group", settings.group);
    const target = "https://ballchasing.com/api/v2/upload?" + params.toString();
    const proxied = "/api/plugin-fetch/ballchasing-upload?url="
      + encodeURIComponent(target);

    let resp;
    try {
      resp = await fetch(proxied, {
        method: "POST",
        headers: { Authorization: settings.apiKey },
        body: fd,
      });
    } catch (e) {
      entry.attempts = (entry.attempts || 0) + 1;
      if (entry.attempts >= 3) {
        entry.status = "failed_permanent";
        entry.lastError = "network: " + e.message;
      } else {
        entry.status = "retrying";
        entry.lastError = "network: " + e.message;
        entry.nextAttemptAt = Date.now() + backoffMs(entry.attempts);
      }
      await saveQueue(queue);
      return;
    }

    if (resp.status === 201 || resp.status === 409) {
      const body = await resp.json().catch(() => ({}));
      entry.status = resp.status === 409 ? "success_duplicate" : "success";
      entry.ballchasingUrl = body.id ? ("https://ballchasing.com/replay/" + body.id) : "";
    } else if (resp.status === 401) {
      entry.status = "failed_permanent";
      entry.lastError = "invalid API key";
    } else if (resp.status === 400) {
      entry.lastError = "bad request: " + (await resp.text().catch(() => ""));
      entry.status = "failed_permanent";
    } else if (resp.status === 429) {
      const raw = parseInt(resp.headers.get("Retry-After") || "60", 10);
      const seconds = Number.isFinite(raw) ? Math.max(1, Math.min(3600, raw)) : 60;
      entry.status = "retrying";
      entry.nextAttemptAt = Date.now() + seconds * 1000;
      entry.lastError = "rate limited";
    } else if (resp.status >= 500) {
      entry.attempts = (entry.attempts || 0) + 1;
      if (entry.attempts >= 3) {
        entry.status = "failed_permanent";
        entry.lastError = "server " + resp.status;
      } else {
        entry.status = "retrying";
        entry.nextAttemptAt = Date.now() + backoffMs(entry.attempts);
        entry.lastError = "server " + resp.status;
      }
    } else {
      entry.status = "failed_permanent";
      entry.lastError = "unexpected status: " + resp.status;
    }
    await saveQueue(queue);
  }

  async function initBackgroundView() {
    queue = await loadQueue();

    RLT.on("_SavedReplay", async (payload) => {
      // Skip if we already have a successful entry for this exact path.
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
      await saveQueue(queue);
      pump();
    });

    // Also re-pump when settings change — saving a new API key should
    // unblock entries parked under the "no apiKey" branch.
    RLT.store.onChange(SETTINGS_KEY, () => { pump(); });

    // Drain any pending/retrying entries from a prior session.
    pump();
  }

  // ── Dispatch ────────────────────────────────────────────────
  RLT.plugin.register({
    name: "ballchasing-upload",
    init() {
      if (view === "settings") {
        initSettingsView();
      } else if (view === "background") {
        initBackgroundView();
      }
      // Other views (overlay stub) do nothing.
    },
  });
})();
