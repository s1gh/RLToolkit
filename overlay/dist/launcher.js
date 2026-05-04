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
const settingsModal = document.getElementById("settings-modal");
const settingsHint = document.getElementById("settings-hint");
const pluginsDirInput = document.getElementById("plugins-dir");
const dataDirInput = document.getElementById("data-dir");

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
  btn.addEventListener("click", async () => {
    if (btn.dataset.cmd === "open_settings") {
      await openSettings();
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
  } catch (_) {
    pluginsDirInput.value = "";
    dataDirInput.value = "";
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
      const dialog = window.__TAURI__?.dialog;
      if (!dialog) return;
      const picked = await dialog.open({ directory: true, multiple: false });
      if (picked) document.getElementById(targetId).value = picked;
    } catch (_) {}
  });
});

document.getElementById("settings-save").addEventListener("click", async () => {
  const plugins = pluginsDirInput.value.trim();
  const data = dataDirInput.value.trim();
  try {
    const respawned = await invoke("save_settings", {
      pluginsDir: plugins || null,
      dataDir: data || null,
    });
    if (respawned) {
      closeSettings();
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
    document.getElementById("dashboard").src = url || "http://localhost:8080/";
  } catch (_) {
    document.getElementById("dashboard").src = "http://localhost:8080/";
  }
}

setInterval(refreshStatus, 2000);
refreshStatus();
loadDashboard();
