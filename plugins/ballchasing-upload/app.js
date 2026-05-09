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

  RLT.plugin.register({
    name: "ballchasing-upload",
    init() {
      // Real wiring lands in subsequent tasks.
      console.log("[ballchasing-upload] " + view + " loaded");
    },
  });
})();
