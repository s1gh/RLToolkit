// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const SETTINGS_KEY = 'settings';
  const DEFAULTS = { showStreak: true };

  function loadSettings(store) {
    return store.get(SETTINGS_KEY).then((s) => Object.assign({}, DEFAULTS, s || {}));
  }
  function saveSettings(store, s) {
    return store.set(SETTINGS_KEY, s);
  }

  async function mount(root) {
    const handle = window.SessionTracker && window.SessionTracker._handle;
    // Fall back to RLT.store if the handle wasn't exposed (defensive — Task 13
    // depends on Task 9's `_handle` export. If it ever races, this still works
    // because RLT.store is plugin-scoped when sdk.js was loaded with data-plugin).
    const store = (handle && handle.store) || RLT.store;

    let s = await loadSettings(store);

    root.innerHTML = `
      <div class="st-settings">
        <h2>Session Tracker — Settings</h2>

        <label class="st-field st-row-toggle">
          <input type="checkbox" id="st-streak" ${s.showStreak ? 'checked' : ''} />
          <span>Show streak badge</span>
        </label>

        <div class="st-field">
          <button id="st-reset" type="button" class="st-danger">Reset session now</button>
        </div>

        <div class="st-actions">
          <button id="st-done" type="button">Done</button>
        </div>
      </div>
    `;

    const sw   = root.querySelector('#st-streak');
    const rst  = root.querySelector('#st-reset');
    const done = root.querySelector('#st-done');

    sw.addEventListener('change', async () => {
      s.showStreak = sw.checked;
      await saveSettings(store, s);
      if (window.SessionTrackerOverlay && window.SessionTrackerOverlay.setSetting) {
        window.SessionTrackerOverlay.setSetting('showStreak', s.showStreak);
      }
      if (window._sessionTrackerRender) window._sessionTrackerRender();
    });

    rst.addEventListener('click', async () => {
      if (!confirm('Clear all session stats? This cannot be undone.')) return;
      const cur = (await store.get('session')) || {};
      const fresh = {
        bootId: cur.bootId || '',
        startedAt: new Date().toISOString(),
        matches: [],
        totals: window.SessionTrackerState
          ? Object.assign(window.SessionTrackerState.recomputeTotals([]), {
              fastestGoalSec: null, hardestShotKmh: null,
              demosGiven: 0, demosReceived: 0, ownGoals: 0,
            })
          : {},
      };
      await store.set('session', fresh);
      if (window.SessionTracker) {
        // Patch the in-memory bucket too (the views read from it).
        const live = window.SessionTracker.state();
        if (live) {
          live.matches.length = 0;
          live.totals = fresh.totals;
          live.startedAt = fresh.startedAt;
        }
      }
      if (window._sessionTrackerRender) window._sessionTrackerRender();
      if (RLT.ui && RLT.ui.toast) RLT.ui.toast('Session reset', 1500);
    });

    done.addEventListener('click', () => {
      if (RLT.settings && RLT.settings.close) RLT.settings.close();
    });
  }

  window.SessionTrackerSettings = { mount };
})();
