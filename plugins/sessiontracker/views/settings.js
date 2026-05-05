// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const SETTINGS_KEY = 'settings';
  const DEFAULTS = { showStreak: true };

  function loadSettings() {
    return RLT.store.get(SETTINGS_KEY).then((s) => Object.assign({}, DEFAULTS, s || {}));
  }
  function saveSettings(s) {
    return RLT.store.set(SETTINGS_KEY, s);
  }

  async function mount(root) {
    let s = await loadSettings();

    root.innerHTML = `
      <div class="st-settings">
        <section class="st-set-section">
          <h3>Display</h3>
          <label class="st-toggle">
            <input type="checkbox" id="st-streak" ${s.showStreak ? 'checked' : ''} />
            <span class="st-toggle-track"><span class="st-toggle-thumb"></span></span>
            <span class="st-toggle-label">
              <span class="st-toggle-title">Streak badge</span>
              <span class="st-toggle-hint">Show a Wn / Ln chip next to the score when on a run.</span>
            </span>
          </label>
        </section>

        <section class="st-set-section">
          <h3>Session</h3>
          <div class="st-set-row">
            <div class="st-set-row-text">
              <span class="st-set-row-title">Reset session</span>
              <span class="st-set-row-hint">Clear matches and stats for the current launcher session. The session continues — only the numbers reset.</span>
            </div>
            <button id="st-reset" type="button" class="st-danger">Reset</button>
          </div>
        </section>
      </div>
    `;

    const sw  = root.querySelector('#st-streak');
    const rst = root.querySelector('#st-reset');

    sw.addEventListener('change', async () => {
      s.showStreak = sw.checked;
      await saveSettings(s);
      if (window.SessionTrackerOverlay && window.SessionTrackerOverlay.setSetting) {
        window.SessionTrackerOverlay.setSetting('showStreak', s.showStreak);
      }
    });

    rst.addEventListener('click', async () => {
      if (!confirm('Clear all session stats? This cannot be undone.')) return;
      const cur = (await RLT.store.get('session')) || {};
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
      await RLT.store.set('session', fresh);
      if (window.SessionTracker) {
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
  }

  window.SessionTrackerSettings = { mount };
})();
