// Shared config helpers loaded by overlay, dashboard, and settings.
// Reading + writing the plugin's RLT.store goes through here so the
// three views can't drift on defaults or merge semantics. Exports
// onto window.HW.
//
// Loaded as a classic <script>, not an ES module — strict mode is
// opt-in inside the IIFE.
// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const STORE_KEY = 'config';
  const DEFAULTS = { logOwnGoals: false };

  async function readConfig() {
    const cfg = await RLT.store.get(STORE_KEY);
    return Object.assign({}, DEFAULTS, cfg || {});
  }

  async function writeConfig(patch) {
    const cfg = await readConfig();
    await RLT.store.set(STORE_KEY, Object.assign({}, cfg, patch));
  }

  window.HW = { readConfig, writeConfig };
})();
