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
  // Persisted under the plugin's RLT.store, key "settings".
  const SETTINGS_KEY = "settings";
  const DEFAULT_SETTINGS = { apiKey: "", visibility: "private", group: "" };

  async function loadSettings() {
    const raw = await RLT.store.get(SETTINGS_KEY);
    return Object.assign({}, DEFAULT_SETTINGS, raw || {});
  }

  async function saveSettings(s) {
    await RLT.store.set(SETTINGS_KEY, s);
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

  // ── Dispatch ────────────────────────────────────────────────
  RLT.plugin.register({
    name: "ballchasing-upload",
    init() {
      if (view === "settings") {
        initSettingsView();
      }
    },
  });
})();
