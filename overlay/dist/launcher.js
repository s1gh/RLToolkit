const invoke = window.__TAURI__.core.invoke;

const $ = sel => document.querySelector(sel);
const conn = $("#conn");
const ownership = $("#ownership-badge");
const overlayToggle = $("#overlay-toggle");
const restartBtn = $("#restart-btn");

async function refreshStatus() {
  try {
    const s = await invoke("get_status");
    conn.dataset.status = s.connected ? "connected" : (s.starting ? "connecting" : "disconnected");
    conn.textContent = conn.dataset.status === "connecting" ? "connecting…" : conn.dataset.status;
    ownership.hidden = !s.attached;
    restartBtn.disabled = s.attached;
    overlayToggle.checked = !!s.overlay_enabled;
  } catch (_) {
    conn.dataset.status = "disconnected";
    conn.textContent = "disconnected";
  }
}

overlayToggle.addEventListener("change", e => {
  invoke("toggle_overlay", { enabled: e.target.checked }).catch(() => {});
});

document.querySelectorAll("[data-cmd]").forEach(btn => {
  btn.addEventListener("click", () => invoke(btn.dataset.cmd).catch(() => {}));
});

setInterval(refreshStatus, 2000);
refreshStatus();
