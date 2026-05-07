// Shared config + audio helpers used by both overlay.html and
// settings.html. Loaded as a classic script; exports onto window.Crossbar.
//
// Kept as a sibling file (not inlined into both HTML pages) so the
// audio + config defaults stay in one place — the settings UI's "Test
// sound" button and the overlay's _CrossbarHit handler must agree on
// volume, preset URLs, and merge semantics.

(function () {
  'use strict';

  const STORE_KEY = 'config';
  const DEFAULTS = {
    selected: 'preset:bonk',
    customDataUrl: null,
    customName: null,
    volume: 0.7,
    playInReplays: true,
  };
  const PRESETS = ['bonk', 'clang', 'airhorn'];
  const MAX_CUSTOM_BYTES = 500 * 1024;

  async function readConfig() {
    const cfg = await RLT.store.get(STORE_KEY);
    return Object.assign({}, DEFAULTS, cfg || {});
  }

  async function writeConfig(patch) {
    const cfg = await readConfig();
    const merged = Object.assign({}, cfg, patch);
    await RLT.store.set(STORE_KEY, merged);
    return merged;
  }

  function resolveSoundUrl(cfg) {
    if (cfg.selected === 'custom' && cfg.customDataUrl) return cfg.customDataUrl;
    let preset = 'bonk';
    if (typeof cfg.selected === 'string' && cfg.selected.startsWith('preset:')) {
      const p = cfg.selected.slice('preset:'.length);
      if (PRESETS.indexOf(p) >= 0) preset = p;
    }
    return 'sounds/' + preset + '.mp3';
  }

  function playSound(cfg) {
    try {
      const audio = new Audio(resolveSoundUrl(cfg));
      audio.volume = Math.max(0, Math.min(1, cfg.volume ?? 0.7));
      const p = audio.play();
      if (p && typeof p.catch === 'function') p.catch(() => {});
    } catch (_) {
      /* construction failures are silent — playback is best-effort */
    }
  }

  window.Crossbar = {
    DEFAULTS,
    PRESETS,
    MAX_CUSTOM_BYTES,
    readConfig,
    writeConfig,
    resolveSoundUrl,
    playSound,
  };
})();
