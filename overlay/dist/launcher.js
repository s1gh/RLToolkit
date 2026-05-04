if (!window.__TAURI__) {
  console.error("__TAURI__ global not injected — launcher webview misconfigured");
}
const invoke = window.__TAURI__?.core?.invoke ?? (() => Promise.reject(new Error("Tauri not ready")));

const $ = sel => document.querySelector(sel);
const conn = $("#conn");
const ownership = $("#ownership-badge");
const overlayToggle = $("#overlay-toggle");
const restartBtn = $("#restart-btn");
const fallback = document.getElementById("fallback");
const fallbackMsg = document.getElementById("fallback-msg");
const fallbackRetry = document.getElementById("fallback-retry");

async function refreshStatus() {
  let s;
  try {
    s = await invoke("get_status");
  } catch (_) {
    return;
  }
  conn.dataset.status = s.connected ? "connected" : (s.starting ? "connecting" : "disconnected");
  conn.textContent = conn.dataset.status === "connecting" ? "connecting…" : conn.dataset.status;
  ownership.hidden = !s.attached;
  restartBtn.disabled = s.attached;
  overlayToggle.checked = !!s.overlay_enabled;

  if (s.body_state === "dashboard") {
    fallback.hidden = true;
  } else {
    fallback.hidden = false;
    fallbackMsg.textContent = s.message || "Backend not responding";
    fallbackRetry.hidden = s.body_state === "starting";
  }

  document.getElementById("tray-banner").hidden = !!s.tray_ok;
}

fallbackRetry.addEventListener("click", () => invoke("restart_backend").catch(() => {}));

overlayToggle.addEventListener("change", e => {
  invoke("toggle_overlay", { enabled: e.target.checked }).catch(() => {});
});

document.querySelectorAll("[data-cmd]").forEach(btn => {
  btn.addEventListener("click", () => invoke(btn.dataset.cmd).catch(() => {}));
});

async function loadDashboard() {
  try {
    const url = await invoke("get_toolkit_url");
    document.getElementById("dashboard").src = url || "http://localhost:8080/";
  } catch (_) {
    document.getElementById("dashboard").src = "http://localhost:8080/";
  }
}

setInterval(refreshStatus, 2000);
refreshStatus();
loadDashboard();
