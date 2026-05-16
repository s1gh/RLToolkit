if (!window.__TAURI__) {
  console.error("__TAURI__ global not injected — launcher webview misconfigured");
}
const invoke = window.__TAURI__?.core?.invoke ?? (() => Promise.reject(new Error("Tauri not ready")));

// Suppress WebKit's built-in context menu (Back / Reload / Open Frame
// in New Window). The launcher is a desktop app, not a browser tab.
window.addEventListener("contextmenu", (e) => e.preventDefault());

const $ = sel => document.querySelector(sel);
const conn = $("#conn");
const ownership = $("#ownership-badge");
const overlayToggle = $("#overlay-toggle");
const restartBtn = $("#restart-btn");
const toggleBackendBtn = $("#toggle-backend-btn");
const fallback = document.getElementById("fallback");
const fallbackCard = document.getElementById("fallback-card");
const fallbackTitle = document.getElementById("fallback-title");
const fallbackMsg = document.getElementById("fallback-msg");
const fallbackRetry = document.getElementById("fallback-retry");

// Tracks the most recent body_state we rendered so the fallback button
// can be re-enabled the instant body_state moves (e.g. stopped →
// starting after the user clicked Start). The click handler below also
// arms `fallbackResetTimer` as a stuck-state safety net so a silently
// failing action doesn't leave the button permanently disabled.
let lastBodyState = null;
let fallbackResetTimer = null;

// Per-state copy + button label for the fallback card. body_state
// arrives from the launcher's Rust StatusView (see launcher/ipc.rs).
// We intentionally do NOT echo s.message into the card body: that field
// often re-states the title verbatim (e.g. body_state=stopped sends
// "Backend stopped." which would render right under a "Backend stopped"
// heading) and the Rust side has no UX context for a two-line layout.
// The `body` strings here are the actionable hint specific to each
// state — what the user should do next, not just what happened.
const FALLBACK_COPY = {
  starting: {
    title:  "Starting backend",
    body:   "Hang tight — this usually takes a few seconds.",
    btn:    null,
    action: null,
  },
  stopped: {
    title:  "Backend stopped",
    body:   "The toolkit isn't running. Click Start to bring it back online.",
    btn:    "Start",
    action: "start",
  },
  crashed: {
    title:  "Backend crashed",
    body:   "The toolkit exited unexpectedly. Restart to try again.",
    btn:    "Restart",
    action: "restart",
  },
  not_responding: {
    title:  "Backend not responding",
    body:   "The toolkit is up but isn't answering. Retrying usually clears it.",
    btn:    "Retry",
    action: "restart",
  },
  port_conflict: {
    title:  "Port in use",
    body:   "Something else is already listening on the toolkit port. Close it, or change the port in Settings.",
    btn:    "Retry",
    action: "restart",
  },
};
const settingsModal = document.getElementById("settings-modal");
const settingsHint = document.getElementById("settings-hint");
const pluginsDirInput = document.getElementById("plugins-dir");
const dataDirInput = document.getElementById("data-dir");
const rlAddrInput = document.getElementById("rl-addr");
const surfWInput = document.getElementById("surface-w");
const surfHInput = document.getElementById("surface-h");
const surfSaveBtn = document.getElementById("surface-save");
const surfDetectedBtn = document.getElementById("surface-use-detected");
const surfClearBtn = document.getElementById("surface-clear");
const surfEffLabel = document.getElementById("surface-effective-label");
const surfDetail = document.getElementById("surface-detail");

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

// ─── Overlay surface client ──────────────────────────────────
// Same shape the dashboard used to ship — three endpoints:
//   GET  /api/overlay/surface          { configured, detected, effective }
//   PUT  /api/overlay/surface          body: {width,height} or null to clear
// `surfaceState` is cached between renders so the buttons can read
// `detected` without a round-trip.
let surfaceState = null;

async function loadSurface() {
  try {
    const base = await toolkitUrl();
    // Refresh "detected" before reading state. The launcher window
    // always knows its monitor; posting it here keeps the value
    // current even after the user clicks Clear (which only nukes
    // `configured`, not `detected`, but a stale detected from
    // session start would still be wrong if the user moved monitors
    // or unplugged a display in between).
    try {
      const mon = await invoke("get_launcher_monitor_size");
      if (mon && mon.width && mon.height) {
        await fetch(base + "/api/overlay/surface/detected", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ width: mon.width, height: mon.height }),
        });
      }
    } catch (e) {
      // Detection is best-effort — backend may be down, monitor info
      // unavailable on this platform, etc. Fall through to the GET.
      console.warn("[launcher] detected-surface refresh failed", e);
    }
    const r = await fetch(base + "/api/overlay/surface");
    if (!r.ok) throw new Error("HTTP " + r.status);
    surfaceState = await r.json();
  } catch (err) {
    console.warn("[launcher] surface fetch failed", err);
    surfaceState = { configured: null, detected: null, effective: { width: 1920, height: 1080 } };
  }
  renderSurface();
}

function renderSurface() {
  if (!surfaceState) return;
  const cfg = surfaceState.configured;
  const det = surfaceState.detected;
  const eff = surfaceState.effective;
  surfWInput.value = cfg ? cfg.width : eff.width;
  surfHInput.value = cfg ? cfg.height : eff.height;
  surfEffLabel.textContent = eff.width + " × " + eff.height;
  surfDetectedBtn.disabled = !det;
  let detail = "";
  if (det) detail += "Detected: " + det.width + " × " + det.height + ". ";
  if (cfg && det && (cfg.width !== det.width || cfg.height !== det.height)) {
    detail += '<span class="mismatch">Configured differs from detected.</span>';
  } else if (!cfg) {
    detail += "No configured size — using " + (det ? "detected" : "fallback") + ".";
  }
  surfDetail.innerHTML = detail;
}

async function putSurface(body) {
  const base = await toolkitUrl();
  const r = await fetch(base + "/api/overlay/surface", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    const msg = (await r.text()).trim();
    throw new Error("HTTP " + r.status + ": " + msg);
  }
  surfaceState = await r.json();
  renderSurface();
}

surfSaveBtn.addEventListener("click", async () => {
  const w = parseInt(surfWInput.value, 10);
  const h = parseInt(surfHInput.value, 10);
  if (!w || !h) {
    settingsHint.textContent = "Surface width and height must be positive integers.";
    settingsHint.hidden = false;
    return;
  }
  try {
    await putSurface({ width: w, height: h });
  } catch (err) {
    settingsHint.textContent = "Save surface failed: " + (err?.message || err);
    settingsHint.hidden = false;
  }
});

surfDetectedBtn.addEventListener("click", async () => {
  if (!surfaceState?.detected) return;
  try {
    await putSurface({ width: surfaceState.detected.width, height: surfaceState.detected.height });
  } catch (err) {
    settingsHint.textContent = "Save surface failed: " + (err?.message || err);
    settingsHint.hidden = false;
  }
});

surfClearBtn.addEventListener("click", async () => {
  try {
    await putSurface(null);
  } catch (err) {
    settingsHint.textContent = "Clear surface failed: " + (err?.message || err);
    settingsHint.hidden = false;
  }
});

// ─── Settings: edit-mode shortcut capture ─────────────────────
// The capture control listens for keydown while focused, builds a
// canonical "Ctrl+Shift+KeyE"-style string from e.code (layout-
// independent — the global hotkey backend keys on physical scancodes
// too, so matching against e.code is what survives keyboard-layout
// changes), and posts it to the backend. The backend tries to bind
// the new combo BEFORE persisting, so a bogus combo can't strand
// the user without a shortcut.
const isMac = navigator.platform.toLowerCase().includes("mac");
const shortcutCaptureBtn = document.getElementById("shortcut-capture");
const shortcutDisplay = document.getElementById("shortcut-display");
const shortcutResetBtn = document.getElementById("shortcut-reset");
const shortcutHint = document.getElementById("shortcut-hint");
const shortcutDefaultLabel = document.getElementById("shortcut-default-label");

let shortcutState = { current: "", stored: null, default: "" };
let shortcutCapturing = false;

function tokenToDisplay(token) {
  // Friendly labels for both the parser's accepted forms (KeyE,
  // Digit1, Numpad0, F5, …) and the modifier names.
  if (/^Key[A-Z]$/.test(token)) return token.slice(3);
  if (/^Digit\d$/.test(token)) return token.slice(5);
  if (/^Numpad(\d|.+)$/.test(token)) return "Num" + token.slice(6);
  const m = {
    Ctrl: isMac ? "⌃" : "Ctrl",
    Control: isMac ? "⌃" : "Ctrl",
    Shift: isMac ? "⇧" : "Shift",
    Alt: isMac ? "⌥" : "Alt",
    Super: isMac ? "⌘" : "Super",
    Meta: isMac ? "⌘" : "Super",
    Cmd: "⌘",
    ArrowUp: "↑", ArrowDown: "↓", ArrowLeft: "←", ArrowRight: "→",
    Space: "Space", Escape: "Esc", Enter: "Enter", Tab: "Tab",
    Backspace: "Backspace", Delete: "Del",
    Equal: "=", Minus: "-", Comma: ",", Period: ".", Slash: "/",
    Backslash: "\\", Quote: "'", Semicolon: ";",
    BracketLeft: "[", BracketRight: "]", Backquote: "`",
  };
  return m[token] || token;
}

function formatCombo(combo) {
  if (!combo) return "—";
  const sep = isMac ? " " : "+";
  return combo.split("+").map(tokenToDisplay).join(sep);
}

function renderShortcut() {
  shortcutDisplay.textContent = formatCombo(shortcutState.current);
  shortcutDefaultLabel.textContent = "Default: " + formatCombo(shortcutState.default);
  // "Reset" only makes sense when a non-default value is stored.
  shortcutResetBtn.disabled = !shortcutState.stored;
}

function setShortcutHint(text, isError) {
  shortcutHint.textContent = text || "";
  shortcutHint.classList.toggle("error", !!isError);
}

async function loadShortcut() {
  try {
    shortcutState = await invoke("get_edit_mode_shortcut");
  } catch (e) {
    console.warn("[launcher] get_edit_mode_shortcut failed", e);
    shortcutState = { current: "Ctrl+Shift+KeyE", stored: null, default: "Ctrl+Shift+KeyE" };
  }
  renderShortcut();
  setShortcutHint("Click and press a key combination.", false);
}

async function saveShortcut(combo) {
  try {
    shortcutState = await invoke("set_edit_mode_shortcut", { combo });
    renderShortcut();
    setShortcutHint("Saved.", false);
  } catch (e) {
    setShortcutHint(String(e?.message || e), true);
    // Refresh from backend so the UI matches whatever the rollback
    // restored, rather than the failed candidate.
    await loadShortcut();
  }
}

function stopCapture() {
  shortcutCapturing = false;
  shortcutCaptureBtn.classList.remove("capturing");
  renderShortcut();
}

function isModifierKey(code) {
  return (
    code === "ControlLeft" || code === "ControlRight" ||
    code === "ShiftLeft" || code === "ShiftRight" ||
    code === "AltLeft" || code === "AltRight" ||
    code === "MetaLeft" || code === "MetaRight" ||
    code === "OSLeft" || code === "OSRight"
  );
}

function buildComboFromEvent(e) {
  const mods = [];
  if (e.ctrlKey) mods.push("Ctrl");
  if (e.shiftKey) mods.push("Shift");
  if (e.altKey) mods.push("Alt");
  if (e.metaKey) mods.push(isMac ? "Cmd" : "Super");
  return { mods, code: e.code };
}

shortcutCaptureBtn.addEventListener("click", () => {
  shortcutCapturing = true;
  shortcutCaptureBtn.classList.add("capturing");
  shortcutDisplay.textContent = "Press a key combination…";
  setShortcutHint("Esc to cancel.", false);
  shortcutCaptureBtn.focus();
});

shortcutCaptureBtn.addEventListener("blur", () => {
  if (shortcutCapturing) stopCapture();
});

shortcutCaptureBtn.addEventListener("keydown", (e) => {
  if (!shortcutCapturing) return;
  // Escape always cancels, regardless of modifier state.
  if (e.code === "Escape" && !e.ctrlKey && !e.shiftKey && !e.altKey && !e.metaKey) {
    e.preventDefault();
    stopCapture();
    setShortcutHint("Cancelled.", false);
    return;
  }
  e.preventDefault();
  e.stopPropagation();

  // Live preview while only modifiers are held — gives feedback that
  // capture is working before the user lands the non-modifier key.
  if (isModifierKey(e.code)) {
    const { mods } = buildComboFromEvent(e);
    shortcutDisplay.textContent = mods.length
      ? mods.map(tokenToDisplay).join(isMac ? " " : "+") + (isMac ? " " : "+") + "…"
      : "Press a key combination…";
    return;
  }

  const { mods, code } = buildComboFromEvent(e);
  // Require at least one non-Shift modifier. A bare letter or
  // Shift+letter would collide with normal typing the moment the
  // user focuses any input, anywhere on the system.
  const hasRealMod = mods.some((m) => m !== "Shift");
  if (!hasRealMod) {
    setShortcutHint("Use at least Ctrl, Alt, or " + (isMac ? "Cmd" : "Super") + ".", true);
    return;
  }

  const combo = [...mods, code].join("+");
  stopCapture();
  saveShortcut(combo);
});

shortcutResetBtn.addEventListener("click", () => {
  if (shortcutCapturing) stopCapture();
  saveShortcut(null);
});

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
    base + "/events?events=_RosterChanged,_IdentityChanged,_OverridesChanged",
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
    } else if (env.Event === "_OverridesChanged") {
      // A plugin was toggled enabled/disabled. Re-sync background
      // workers so a newly-enabled plugin starts immediately and a
      // newly-disabled one stops.
      syncBackgroundWorkers();
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
// few polls so the badge doesn't flap on every idle cycle. The hold only
// kicks in AFTER we've actually seen a "connected" — without this gate
// the cold-start case (RL not running, rl_api never connected) would
// briefly show "connected" before flipping to "game disconnected" once
// the miss counter caught up.
let rlApiMisses = 0;
let rlApiEverConnected = false;
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
    rlApiEverConnected = true;
  } else {
    rlApiMisses++;
  }
  const rlApiConnected =
    s.rl_api === "connected" ||
    (rlApiEverConnected && rlApiMisses < RL_API_DEBOUNCE);

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
    const copy = FALLBACK_COPY[s.body_state] || FALLBACK_COPY.not_responding;
    fallbackCard.dataset.state = s.body_state;
    fallbackTitle.textContent = copy.title;
    fallbackMsg.textContent = copy.body;
    if (copy.btn) {
      fallbackRetry.hidden = false;
      fallbackRetry.textContent = copy.btn;
      fallbackRetry.dataset.action = copy.action;
    } else {
      fallbackRetry.hidden = true;
    }
  }
  // Re-enable the action button as soon as body_state actually moves
  // — the click handler disables it optimistically and the safety
  // timeout (in the click handler) covers the stuck-state case.
  if (s.body_state !== lastBodyState) {
    fallbackRetry.disabled = false;
    if (fallbackResetTimer) {
      clearTimeout(fallbackResetTimer);
      fallbackResetTimer = null;
    }
    lastBodyState = s.body_state;
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
  // Hide while the about:blank hop and the real-URL load complete.
  iframe.classList.remove("loaded");
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

  // Refresh background workers too. Their lifecycle is independent of
  // the dashboard iframe but the same triggers (backend reconnect,
  // settings save) should re-sync them.
  syncBackgroundWorkers();
}

// ─── Background workers ────────────────────────────────────
// Plugins whose manifest declares `background.file` get a hidden
// iframe loaded for as long as the launcher is open AND the plugin
// is enabled. Used by plugins that need to react to events
// independent of any visible UI surface (e.g. ballchasing-upload's
// uploader pump runs the moment _SavedReplay fires, regardless of
// whether the user has its dashboard tab open).
async function syncBackgroundWorkers() {
  const host = document.getElementById("bg-workers");
  if (!host) return;
  let base;
  try {
    base = await toolkitUrl();
  } catch (_) {
    return;
  }
  let plugins, overrides;
  try {
    [plugins, overrides] = await Promise.all([
      fetch(base + "/api/plugins").then(r => r.json()).catch(() => []),
      fetch(base + "/api/overlay/overrides").then(r => r.json()).catch(() => ({})),
    ]);
  } catch (_) {
    return;
  }

  const wanted = new Set();
  for (const p of plugins || []) {
    if (!p || !p.background || !p.background.file) continue;
    const ov = overrides && overrides[p.name];
    if (!ov || ov.enabled !== true) continue;
    wanted.add(p.name);
  }

  // Remove iframes for plugins no longer wanted.
  for (const child of Array.from(host.children)) {
    const name = child.dataset.plugin;
    if (!name || !wanted.has(name)) {
      child.remove();
    } else {
      wanted.delete(name);
    }
  }

  // Add iframes for newly-wanted plugins.
  for (const name of wanted) {
    const p = (plugins || []).find(x => x && x.name === name);
    if (!p || !p.background || !p.background.file) continue;
    const ifr = document.createElement("iframe");
    ifr.dataset.plugin = name;
    ifr.title = name + " (background)";
    ifr.style.display = "none";
    ifr.src = base + "/plugins/" + encodeURIComponent(name) + "/" + p.background.file;
    host.appendChild(ifr);
  }
}

// Disable the fallback button immediately on click so the user gets
// visible feedback that the action registered. The status poll runs
// every POLL_FAST_MS=2s, so without this the button looks idle for up
// to two seconds while the request is in flight. The button is
// re-enabled when body_state actually changes (in the status handler
// above — see `if (s.body_state !== lastBodyState)`), or by the safety
// timeout below if the action silently failed and the state is stuck.
fallbackRetry.addEventListener("click", () => {
  if (fallbackRetry.disabled) return;
  fallbackRetry.disabled = true;
  const cmd = fallbackRetry.dataset.action === "start" ? "start_backend" : "restart_backend";
  invoke(cmd).catch(() => {});
  // Safety net: if the next ~5s of status polls don't move body_state
  // (e.g. start_backend failed silently, or the backend is wedged), the
  // user is otherwise stranded on a permanently-disabled button. 5s is
  // ~2.5 fast-poll cycles — enough headroom for a healthy transition.
  if (fallbackResetTimer) clearTimeout(fallbackResetTimer);
  fallbackResetTimer = setTimeout(() => {
    fallbackRetry.disabled = false;
    fallbackResetTimer = null;
  }, 5000);
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

// Window controls — minimize / toggle-maximize / close. With
// decorations(false) we own the title bar, so these HTML buttons
// drive the platform window via __TAURI__.window. Close goes
// through the standard close path: the launcher's CloseRequested
// handler hides to tray when one is available (same as the OS ×).
// Maximize toggles between maximized and the prior windowed size.
const winApi = window.__TAURI__?.window;
document.querySelectorAll("[data-window-cmd]").forEach((btn) => {
  btn.addEventListener("click", async () => {
    if (!winApi) return;
    // Drop focus immediately so hover/active states settle cleanly
    // and we don't keep painting any residual focus indicator.
    btn.blur();
    const cmd = btn.dataset.windowCmd;
    try {
      const w = winApi.getCurrentWindow();
      if (cmd === "minimize") await w.minimize();
      else if (cmd === "toggleMaximize") await w.toggleMaximize();
      else if (cmd === "close") await w.close();
    } catch (e) {
      // The Tauri ops can race a hide-to-tray transition; swallow.
      console.warn("window cmd failed", cmd, e);
    }
  });
});

// Native <details> doesn't close on outside-click; wire that here.
// Capture phase so a click that hits any other interactive element
// (window controls, dashboard iframe blur, splash buttons) still
// dismisses the menu first. Pointerdown rather than click so the
// dismissal beats the pointerup-based focus changes.
document.addEventListener("pointerdown", (e) => {
  const menu = document.getElementById("overflow");
  if (!menu || !menu.hasAttribute("open")) return;
  if (menu.contains(e.target)) return;
  menu.removeAttribute("open");
}, true);

// Esc closes the menu too — matches every other dropdown convention.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") return;
  const menu = document.getElementById("overflow");
  if (menu?.hasAttribute("open")) menu.removeAttribute("open");
});

// Iframe-aware dismissal: the dashboard runs in its own document, so
// pointerdown there never bubbles to this listener. When the user
// clicks into the iframe the launcher window loses focus, and that's
// the signal we use to close the menu.
window.addEventListener("blur", () => {
  const menu = document.getElementById("overflow");
  if (menu?.hasAttribute("open")) menu.removeAttribute("open");
});

// Click-to-focus mitigation. On Wayland (Hyprland especially) the
// transition between the Tauri shell and the dashboard iframe doesn't
// always complete in one event. After dismissing the launcher's
// overflow menu by clicking into the iframe area, the next click on
// a button inside the dashboard can be eaten by the compositor as a
// focus-grab, so the user has to click twice. Forwarding focus into
// the iframe whenever the launcher window regains focus shortcuts
// the second click. focus() on a same-process cross-origin iframe is
// allowed; we don't read into the iframe, only nudge focus.
window.addEventListener("focus", () => {
  const iframe = document.getElementById("dashboard");
  iframe?.contentWindow?.focus();
});

// Frameless resize: each .rh zone calls startResizeDragging on
// mousedown with its compass direction. Tauri's API takes the
// direction as a string (e.g. "North", "SouthEast"); we stash it on
// data-resize. preventDefault stops the webview from also kicking
// off a text-selection drag in parallel.
document.querySelectorAll("[data-resize]").forEach((zone) => {
  zone.addEventListener("mousedown", (e) => {
    if (!winApi || e.button !== 0) return;
    e.preventDefault();
    try {
      winApi.getCurrentWindow().startResizeDragging(zone.dataset.resize);
    } catch (err) {
      console.warn("startResizeDragging failed", err);
    }
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
  // Surface fields read from the backend (not invoke), so they take a
  // round-trip — fire-and-forget; renderSurface() updates the modal in
  // place when the response lands. Errors fall back to a "1920×1080"
  // placeholder so the inputs are never empty.
  loadSurface();
  loadShortcut();
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
    // Returns { changed, respawned }. Three branches:
    //   • not changed       → backend untouched, just close.
    //   • changed, respawned → launcher restarted the sidecar; reload
    //     the dashboard (suppress the polling-driven reload first so
    //     we don't trigger two reloads back to back).
    //   • changed, !respawned → attached mode, settings hit disk but
    //     the backend wasn't touched; tell the user to restart.
    const result = await invoke("save_settings", {
      pluginsDir: plugins || null,
      dataDir: data || null,
      rlAddr: rlAddr || null,
    });
    if (!result.changed) {
      closeSettings();
      return;
    }
    if (result.respawned) {
      closeSettings();
      suppressReloadUntil = Date.now() + 5000;
      setTimeout(() => { reloadDashboard(); }, 800);
      return;
    }
    settingsHint.textContent = "Settings saved — restart your backend to apply.";
    settingsHint.hidden = false;
  } catch (e) {
    settingsHint.textContent = "Save failed: " + (e?.message || e);
    settingsHint.hidden = false;
  }
});

async function loadDashboard() {
  const iframe = document.getElementById("dashboard");
  // Clear the loaded class before navigating so the iframe fades back
  // out for the navigation, then back in once the new document fires
  // its load event. wireDashboardLoadGate (below) handles re-adding
  // the class. Without this the first about:blank → real-URL hop
  // would briefly show the iframe's default white canvas.
  iframe?.classList.remove("loaded");
  try {
    const url = await invoke("get_toolkit_url");
    iframe.src = url || "http://localhost:49200/";
  } catch (_) {
    iframe.src = "http://localhost:49200/";
  }
  // Spin up background workers for any enabled plugin that declares
  // `background.file`. reloadDashboard does this too, but that's only
  // called on backend reconnect / settings-save; loadDashboard is the
  // entry path on a fresh launch with an established identity.
  syncBackgroundWorkers();
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

// Adaptive status polling. The launcher doesn't need 2s cadence in
// steady state — once we've seen "connected" we slow to 10s, and only
// tighten back to 2s when something goes wrong (disconnect, "starting"
// transitions). The whole timer also pauses while the window is hidden
// (minimized / on another workspace) so the launcher doesn't compete
// with Rocket League for the same GPU/CPU when the user can't see it
// anyway.
const POLL_FAST_MS = 2000;
const POLL_SLOW_MS = 10000;
let pollTimer = null;
let pollCurrentMs = POLL_FAST_MS;

function desiredPollInterval() {
  // Slow path only when everything's nominal AND the dashboard is up.
  // Anything else (splash, starting, disconnected, attached races)
  // wants the faster cadence so the UI reacts promptly.
  const status = conn.dataset.status;
  if (splashState === "dashboard" && status === "connected") return POLL_SLOW_MS;
  return POLL_FAST_MS;
}

function schedulePoll() {
  if (document.hidden) return;
  if (pollTimer !== null) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
  pollCurrentMs = desiredPollInterval();
  pollTimer = setInterval(() => {
    refreshStatus().then(() => {
      // Re-evaluate cadence after each tick; reschedule if the
      // desired interval changed.
      const want = desiredPollInterval();
      if (want !== pollCurrentMs) schedulePoll();
    });
  }, pollCurrentMs);
}

function stopPoll() {
  if (pollTimer !== null) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    // Window minimized / hidden — pause the status poll and the SSE
    // stream. Both wake back up on visible. refreshStatus already
    // returns silently on Tauri errors, so any in-flight tick that
    // finishes after we stop is harmless.
    stopPoll();
    closeSse();
  } else {
    // Resume. Run an immediate refresh so the UI catches up to any
    // backend state change that happened while we were hidden, then
    // resume the timer. Re-open SSE if we're back on the splash.
    refreshStatus();
    schedulePoll();
    if (splashState === "splash") openSse();
  }
});

refreshStatus();
schedulePoll();
bootIdentityCheck();

// Forward the launcher version into the embedded dashboard so the
// footer shows it next to "Developer endpoints", on the same baseline.
// The dashboard listens for `rl-launcher:set-version` and renders the
// payload as text. Posts on every iframe load (initial mount + any
// subsequent reload from identity reset, plugin install, etc.).
(function initLauncherVersionBridge() {
  const iframe = document.getElementById("dashboard");
  if (!iframe) return;
  let cached = null;
  async function fetchVersion() {
    if (cached !== null) return cached;
    try {
      cached = String(await invoke("get_app_version") || "");
    } catch {
      cached = "";
    }
    return cached;
  }
  async function post() {
    const version = await fetchVersion();
    if (!version || !iframe.contentWindow) return;
    try {
      iframe.contentWindow.postMessage(
        { type: "rl-launcher:set-version", version },
        "*",
      );
    } catch {}
  }
  iframe.addEventListener("load", () => { post(); });
  // First load may already be in-flight when this listener attaches.
  // contentWindow is non-null once the about:blank has parsed, which
  // is good enough — the dashboard's listener runs on its own load.
  if (iframe.contentWindow) post();
})();

// Reveal the dashboard iframe once a real document has loaded into
// it. The CSS hides #dashboard at opacity:0 by default; we add the
// `loaded` class on `load` so the iframe fades in. Ignoring
// about:blank loads is important: reloadDashboard cycles through
// about:blank to force a re-fetch, and that intermediate frame would
// otherwise unhide the iframe and show a white canvas before the
// real URL kicks in.
(function wireDashboardLoadGate() {
  const iframe = document.getElementById("dashboard");
  if (!iframe) return;
  iframe.addEventListener("load", () => {
    let href = "";
    try { href = iframe.contentWindow?.location?.href || ""; } catch (_) {}
    if (href === "about:blank") return;
    iframe.classList.add("loaded");
  });
})();

// ─── Updater banner ─────────────────────────────────────────
//
// Two paths into the banner:
//   1. Rust's startup check emits `updater://available` once the
//      manifest is fetched. Fast path when the network is quick.
//   2. The frontend itself asks `check_for_updates` on load. Reliable
//      path — the Rust event can race the webview load and arrive
//      before this listener exists, in which case the event is lost
//      and only this query surfaces the banner.
(function initUpdaterBanner() {
  const banner = document.getElementById("updater-banner");
  if (!banner) return;
  const text = banner.querySelector(".updater-text");
  const installBtn = banner.querySelector(".updater-install");
  const dismissBtn = banner.querySelector(".updater-dismiss");

  const invoke = window.__TAURI__?.core?.invoke;
  const listen = window.__TAURI__?.event?.listen;
  if (!invoke || !listen) return;

  function show(version) {
    text.textContent = `Version ${version} is available.`;
    banner.hidden = false;
  }
  function hide() {
    banner.hidden = true;
  }

  listen("updater://available", (e) => show(String(e.payload || "")));

  // Reliable path. Run once at load — Rust's check_for_updates is
  // cheap to call (it does the same network round-trip the startup
  // check did, so within seconds it returns the same answer).
  invoke("check_for_updates")
    .then((res) => {
      if (res && res.kind === "available") {
        show(res.version);
      }
    })
    .catch(() => {
      // Command unavailable on portable builds (no updater feature).
      // Banner stays hidden, which is the right behaviour.
    });

  installBtn.addEventListener("click", async () => {
    installBtn.disabled = true;
    text.textContent = "Downloading update…";
    try {
      await invoke("apply_update");
      // Tauri relaunches us. If we're still here, install errored.
      text.textContent = "Update did not complete. See logs.";
    } catch (err) {
      text.textContent = `Update failed: ${err}`;
    } finally {
      installBtn.disabled = false;
    }
  });

  dismissBtn.addEventListener("click", hide);
})();

