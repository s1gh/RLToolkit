if (!window.__TAURI__) {
  console.error("__TAURI__ global not injected — launcher webview misconfigured");
}
const invoke = window.__TAURI__?.core?.invoke ?? (() => Promise.reject(new Error("Tauri not ready")));

const $ = sel => document.querySelector(sel);
const conn = $("#conn");
const ownership = $("#ownership-badge");
const overlayToggle = $("#overlay-toggle");
const restartBtn = $("#restart-btn");
const toggleBackendBtn = $("#toggle-backend-btn");
const fallback = document.getElementById("fallback");
const fallbackMsg = document.getElementById("fallback-msg");
const fallbackRetry = document.getElementById("fallback-retry");
const settingsModal = document.getElementById("settings-modal");
const settingsHint = document.getElementById("settings-hint");
const pluginsDirInput = document.getElementById("plugins-dir");
const dataDirInput = document.getElementById("data-dir");
const rlAddrInput = document.getElementById("rl-addr");

// ─── Identity client ──────────────────────────────────────────
// Resolves the toolkit URL once and caches it. The launcher hits
// /api/identity directly (no SDK needed) — three small endpoints.
let _toolkitUrl = null;
async function toolkitUrl() {
  if (_toolkitUrl) return _toolkitUrl;
  let raw;
  try {
    raw = (await invoke("get_toolkit_url")) || "http://localhost:49200";
  } catch (_) {
    raw = "http://localhost:49200";
  }
  _toolkitUrl = raw.replace(/\/+$/, "");
  return _toolkitUrl;
}

async function getIdentity() {
  const base = await toolkitUrl();
  const r = await fetch(base + "/api/identity");
  if (!r.ok) return null;
  return r.json().catch(() => null);
}

async function setIdentity(primaryId, name) {
  const base = await toolkitUrl();
  const r = await fetch(base + "/api/identity", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ primaryId, name: name || "" }),
  });
  if (!r.ok) throw new Error("set identity: " + r.status);
}

async function clearIdentity() {
  const base = await toolkitUrl();
  const r = await fetch(base + "/api/identity", { method: "DELETE" });
  if (!r.ok && r.status !== 204) throw new Error("clear identity: " + r.status);
}

// ─── Splash state machine ────────────────────────────────────
const splash = document.getElementById("splash");
const splashStatus = document.getElementById("splash-status");
const splashRoster = document.getElementById("splash-roster");
const splashError = document.getElementById("splash-error");
const splashConfirm = document.getElementById("splash-confirm");
const splashConfirmName = document.getElementById("splash-confirm-name");
const body = document.getElementById("body");

// 'unknown' | 'splash' | 'confirming' | 'dashboard'
let splashState = "unknown";
let sseSource = null;
let lastRoster = [];

function setSplashStatus(text, level) {
  splashStatus.textContent = text;
  splashStatus.classList.remove("warn", "bad");
  if (level === "warn") splashStatus.classList.add("warn");
  if (level === "bad") splashStatus.classList.add("bad");
}

function showSplashError(msg) {
  splashError.textContent = msg;
  splashError.hidden = false;
}

function clearSplashError() {
  splashError.hidden = true;
  splashError.textContent = "";
}

function renderRoster(players) {
  // Filter to humans only — bots can't be "you".
  const humans = (players || []).filter((p) => !p.isBot);
  const multi = humans.length > 1;

  if (humans.length === 0) {
    splashRoster.innerHTML =
      '<div class="splash-status">Roster received but no humans detected — make sure you\'re in the match.</div>';
    return;
  }

  const rows = humans
    .map((p) => {
      const platform = (p.platform || p.id?.split("|")[0] || "?").toLowerCase();
      const cls = "splash-row" + (multi ? " disabled" : "");
      const disabled = multi ? ' aria-disabled="true"' : "";
      return (
        '<button type="button" class="' +
        cls +
        '" data-pid="' +
        escAttr(p.id) +
        '" data-pname="' +
        escAttr(p.name) +
        '"' +
        disabled +
        ">" +
        '<span class="splash-row-name">' +
        escHtml(p.name) +
        "</span>" +
        '<span class="splash-row-platform">' +
        escHtml(platform) +
        "</span>" +
        "</button>"
      );
    })
    .join("");

  const hint = multi
    ? '<div class="splash-status warn">Multiple humans detected — please queue solo so we can be sure which one is you.</div>'
    : "";

  splashRoster.innerHTML = hint + rows;
}

function escAttr(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[c]);
}
function escHtml(s) {
  return String(s == null ? "" : s).replace(/[&<>]/g, (c) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
  })[c]);
}

async function openSse() {
  if (sseSource) return;
  const base = await toolkitUrl();
  // Subscribe just to the two events we care about. The backend's
  // framing-bypass keeps _IdentityChanged / _RosterChanged flowing
  // even with this filter.
  sseSource = new EventSource(
    base + "/events?events=_RosterChanged,_IdentityChanged",
  );
  sseSource.onmessage = (e) => {
    let env;
    try {
      env = JSON.parse(e.data);
    } catch (_) {
      return;
    }
    if (!env || !env.Event) return;
    if (env.Event === "_RosterChanged") {
      lastRoster = (env.Data && env.Data.players) || [];
      renderRoster(lastRoster);
    }
    // _IdentityChanged is informational here — state transitions are
    // driven by the explicit fetch results in claim/reclaim.
  };
  sseSource.onopen = () => {
    syncSplashStatusFromTopbar();
  };
  sseSource.onerror = () => {
    setSplashStatus("Lost connection to the backend. Retrying…", "bad");
  };
}

function closeSse() {
  if (sseSource) {
    sseSource.close();
    sseSource = null;
  }
}

function syncSplashStatusFromTopbar() {
  // Pull the same signal the topbar is showing. If the topbar conn
  // says 'connected' and rl_api is connected, we're waiting on a
  // roster. Otherwise mirror the launcher's existing wording.
  const status = conn.dataset.status;
  if (status === "connected") {
    if (lastRoster.length === 0) {
      setSplashStatus("RL connected · waiting for a roster…");
    }
  } else if (status === "warning") {
    setSplashStatus(
      "RL not detected — start Rocket League and queue a private match.",
      "warn",
    );
  } else if (status === "connecting") {
    setSplashStatus("Connecting to the backend…");
  } else {
    setSplashStatus("Backend not responding.", "bad");
  }
}

async function enterSplash() {
  splashState = "splash";
  body.hidden = true;
  splash.hidden = false;
  splashConfirm.hidden = true;
  clearSplashError();
  splashRoster.innerHTML = "";
  lastRoster = [];
  syncSplashStatusFromTopbar();
  await openSse();
}

function enterDashboard() {
  splashState = "dashboard";
  splash.hidden = true;
  body.hidden = false;
  closeSse();
  loadDashboard();
}

async function bootIdentityCheck() {
  try {
    const id = await getIdentity();
    if (id && id.primaryId) {
      enterDashboard();
    } else {
      await enterSplash();
    }
  } catch (_) {
    // Backend not responding yet. Stay 'unknown'; the status poll will
    // retry on the next tick.
    splashState = "unknown";
  }
}

// Click handler for splash roster rows.
splashRoster.addEventListener("click", async (e) => {
  if (splashState !== "splash") return;
  const row = e.target.closest(".splash-row");
  if (!row || row.classList.contains("disabled")) return;
  const pid = row.dataset.pid;
  const pname = row.dataset.pname || "";
  if (!pid) return;
  clearSplashError();
  try {
    await setIdentity(pid, pname);
  } catch (err) {
    showSplashError("Couldn't save identity: " + (err?.message || err));
    return;
  }
  // Confirming beat → dashboard.
  splashState = "confirming";
  closeSse();
  splashConfirmName.textContent = pname || pid;
  splashConfirm.hidden = false;
  setTimeout(() => {
    if (splashState === "confirming") enterDashboard();
  }, 1500);
});

let lastConnected = false;
let suppressReloadUntil = 0;
let disconnectMisses = 0;
const DISCONNECT_DEBOUNCE = 2; // consecutive failed polls before showing disconnected
// The Go RL client cycles its TCP connection every rlIdleTimeout (30s) when
// RL is idle, so rl_api briefly drops to "connecting"/"disconnected" during
// the ~0.5–5s reconnect window. Hold the last "connected" verdict across a
// few polls so the badge doesn't flap on every idle cycle.
let rlApiMisses = 0;
const RL_API_DEBOUNCE = 3;

async function refreshStatus() {
  let s;
  try {
    s = await invoke("get_status");
  } catch (_) {
    return;
  }

  if (!s.connected) {
    disconnectMisses++;
  } else {
    disconnectMisses = 0;
  }

  const displayConnected = s.connected || disconnectMisses < DISCONNECT_DEBOUNCE;

  if (s.rl_api === "connected") {
    rlApiMisses = 0;
  } else {
    rlApiMisses++;
  }
  const rlApiConnected = s.rl_api === "connected" || rlApiMisses < RL_API_DEBOUNCE;

  if (!displayConnected) {
    conn.dataset.status = s.starting ? "connecting" : "disconnected";
    conn.textContent = s.starting ? "connecting…" : "disconnected";
  } else if (rlApiConnected) {
    conn.dataset.status = "connected";
    conn.textContent = "connected";
  } else {
    conn.dataset.status = "warning";
    conn.textContent = "game " + (s.rl_api || "disconnected");
  }
  ownership.hidden = !s.attached;
  restartBtn.disabled = s.attached;
  overlayToggle.checked = !!s.overlay_enabled;

  if (toggleBackendBtn) {
    toggleBackendBtn.disabled = s.attached;
    toggleBackendBtn.textContent = s.stopped_by_user ? "Start backend" : "Stop backend";
  }

  if (s.body_state === "dashboard") {
    fallback.hidden = true;
  } else {
    fallback.hidden = false;
    fallbackMsg.textContent = s.message || "Backend not responding";
    fallbackRetry.hidden = s.body_state === "starting";
    fallbackRetry.textContent = s.body_state === "stopped" ? "Start" : "Retry";
    fallbackRetry.dataset.action = s.body_state === "stopped" ? "start" : "restart";
  }

  document.getElementById("tray-banner").hidden = !!s.tray_ok;

  // Reload the dashboard iframe whenever the backend transitions from
  // disconnected → connected. Picks up restarts (settings save, manual
  // restart-backend, external respawn) without a stale view. The
  // suppress window prevents a duplicate reload right after save_settings
  // already scheduled one.
  if (
    splashState === "dashboard" &&
    s.connected &&
    !lastConnected &&
    Date.now() >= suppressReloadUntil
  ) {
    reloadDashboard();
  }
  lastConnected = s.connected;
  // While in 'unknown' state, keep retrying the identity check until
  // the backend answers. Once we know the answer we stay in splash or
  // dashboard for the rest of the session.
  if (splashState === "unknown" && s.connected) {
    bootIdentityCheck();
  }
  // While the splash is up, mirror the topbar status into the splash
  // status line (only if no roster is currently displayed).
  if (splashState === "splash" && lastRoster.length === 0) {
    syncSplashStatusFromTopbar();
  }
}

async function reloadDashboard() {
  const iframe = document.getElementById("dashboard");
  if (!iframe) return;
  let url = "http://localhost:49200/";
  try {
    url = (await invoke("get_toolkit_url")) || url;
  } catch (_) {}
  // Force a reload by toggling through about:blank — some webviews skip
  // a reload if iframe.src is reassigned to its current value.
  iframe.src = "about:blank";
  // Use a microtask so the assignment commits before we set the real URL.
  await new Promise(resolve => setTimeout(resolve, 50));
  // Cache-bust so the dashboard's HTML, JS, and CSS are all re-fetched
  // even if the webview's HTTP cache would otherwise serve stale copies.
  const sep = url.includes("?") ? "&" : "?";
  iframe.src = `${url}${sep}_t=${Date.now()}`;
}

fallbackRetry.addEventListener("click", () => {
  const cmd = fallbackRetry.dataset.action === "start" ? "start_backend" : "restart_backend";
  invoke(cmd).catch(() => {});
});

overlayToggle.addEventListener("change", e => {
  invoke("toggle_overlay", { enabled: e.target.checked }).catch(() => {});
});

document.querySelectorAll("[data-cmd]").forEach(btn => {
  btn.addEventListener("click", async () => {
    document.getElementById("overflow")?.removeAttribute("open");
    if (btn.dataset.cmd === "open_settings") {
      await openSettings();
      return;
    }
    if (btn.dataset.cmd === "toggle_backend") {
      // Dispatch to start_backend or stop_backend based on current label.
      const cmd = btn.textContent.trim().toLowerCase().startsWith("start")
        ? "start_backend"
        : "stop_backend";
      invoke(cmd).catch(() => {});
      return;
    }
    if (btn.dataset.cmd === "reclaim_identity") {
      try {
        await clearIdentity();
        await enterSplash();
      } catch (e) {
        console.error("reclaim failed", e);
      }
      return;
    }
    invoke(btn.dataset.cmd).catch(() => {});
  });
});

async function openSettings() {
  try {
    const s = await invoke("get_settings");
    pluginsDirInput.value = s.plugins_dir || "";
    dataDirInput.value = s.data_dir || "";
    rlAddrInput.value = s.rl_addr || "";
    // Show the resolved OS-standard defaults as placeholders so the user
    // can see where files would go if they leave the field blank.
    if (s.default_plugins_dir) pluginsDirInput.placeholder = s.default_plugins_dir;
    if (s.default_data_dir) dataDirInput.placeholder = s.default_data_dir;
  } catch (_) {
    pluginsDirInput.value = "";
    dataDirInput.value = "";
    rlAddrInput.value = "";
  }
  settingsHint.hidden = true;
  settingsHint.textContent = "";
  settingsModal.hidden = false;
}

function closeSettings() {
  settingsModal.hidden = true;
}

document.getElementById("settings-cancel").addEventListener("click", closeSettings);
settingsModal.querySelector(".modal-backdrop").addEventListener("click", closeSettings);

document.querySelectorAll("[data-pick]").forEach(btn => {
  btn.addEventListener("click", async () => {
    const targetId = btn.dataset.pick;
    try {
      const picked = await invoke("plugin:dialog|open", {
        options: { directory: true, multiple: false },
      });
      if (picked) document.getElementById(targetId).value = picked;
    } catch (e) {
      settingsHint.textContent = "Browse failed: " + (e?.message || e);
      settingsHint.hidden = false;
    }
  });
});

document.getElementById("settings-save").addEventListener("click", async () => {
  const plugins = pluginsDirInput.value.trim();
  const data = dataDirInput.value.trim();
  const rlAddr = rlAddrInput.value.trim();
  try {
    const respawned = await invoke("save_settings", {
      pluginsDir: plugins || null,
      dataDir: data || null,
      rlAddr: rlAddr || null,
    });
    if (respawned) {
      closeSettings();
      // Suppress the polling-driven reconnect-reload for ~5s; we'll do
      // the reload explicitly once at +800ms.
      suppressReloadUntil = Date.now() + 5000;
      setTimeout(() => { reloadDashboard(); }, 800);
    } else {
      // Attached — user must restart their backend manually.
      settingsHint.textContent = "Settings saved — restart your backend to apply.";
      settingsHint.hidden = false;
    }
  } catch (e) {
    settingsHint.textContent = "Save failed: " + (e?.message || e);
    settingsHint.hidden = false;
  }
});

async function loadDashboard() {
  try {
    const url = await invoke("get_toolkit_url");
    document.getElementById("dashboard").src = url || "http://localhost:49200/";
  } catch (_) {
    document.getElementById("dashboard").src = "http://localhost:49200/";
  }
}

// Bridge: the embedded dashboard iframe sends external-open requests via
// postMessage so links with target="_blank" can be routed to the user's
// real browser instead of failing silently inside the webview iframe.
window.addEventListener("message", event => {
  const data = event.data;
  if (!data || typeof data !== "object") return;
  if (data.type !== "rl-launcher:open-external") return;
  const url = typeof data.url === "string" ? data.url : null;
  if (!url) return;
  invoke("open_external_url", { url }).catch(() => {});
});

setInterval(refreshStatus, 2000);
refreshStatus();
bootIdentityCheck();
