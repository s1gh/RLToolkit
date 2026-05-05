// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function () {
  const STORE_KEY = 'session';
  const isOverlay  = new URLSearchParams(location.search).has('overlay');
  const isSettings = new URLSearchParams(location.search).has('settings');

  function emptyTotals() {
    return {
      wins: 0, losses: 0,
      goals: 0, assists: 0, saves: 0, shots: 0, demos: 0,
      mvps: 0, timeInMatchesSec: 0,
      currentStreak: null, bestStreak: null,
      hatTricks: 0, aerialGoals: 0, bicycleGoals: 0,
      epicSaves: 0, flipResets: 0, otGoals: 0,
      fastestGoalSec: null, hardestShotKmh: null,
      demosGiven: 0, demosReceived: 0, ownGoals: 0,
    };
  }

  function freshBucket(bootId) {
    return {
      bootId,
      startedAt: new Date().toISOString(),
      matches: [],
      totals: emptyTotals(),
    };
  }

  // Per-match accumulators (reset on each new match).
  let perMatch = newPerMatch();
  function newPerMatch() {
    return { highlights: [], liveDurationSec: 0, lastPhaseTs: null };
  }

  // In-memory cache of the bucket. All writes go through scheduleSave().
  let bucket = null;
  let saveTimer = null;
  let _handleRef = null;

  function scheduleSave(handle) {
    if (saveTimer) return;
    saveTimer = setTimeout(() => {
      saveTimer = null;
      handle.store.set(STORE_KEY, bucket);
    }, 50);
  }

  function scheduleSaveExternal() {
    if (_handleRef) scheduleSave(_handleRef);
  }

  // Resolve current bootId from SSE frame or HTTP fallback.
  function resolveBootID() {
    return new Promise((resolve) => {
      let resolved = false;
      const off = RLT.on('_BootId', (p) => {
        if (resolved) return;
        resolved = true;
        off();
        resolve(p && p.bootId);
      });
      // Fallback after 2s for direct-mode browser sources that may have
      // missed the first frame.
      setTimeout(async () => {
        if (resolved) return;
        try {
          const r = await fetch('/api/boot-id');
          const j = await r.json();
          if (resolved) return;
          resolved = true;
          off();
          resolve(j && j.bootId);
        } catch (_) {
          if (resolved) return;
          resolved = true;
          off();
          resolve(null);
        }
      }, 2000);
    });
  }

  function mountView() {
    const root = document.getElementById('root');
    if (!root) return;
    if (isSettings && window.SessionTrackerSettings) {
      window.SessionTrackerSettings.mount(root);
    } else if (isOverlay && window.SessionTrackerOverlay) {
      window.SessionTrackerOverlay.mount(root);
    } else if (window.SessionTrackerDashboard) {
      window.SessionTrackerDashboard.mount(root);
    } else {
      root.textContent = 'session tracker (view not loaded)';
    }
  }

  RLT.plugin.register({
    async ready(handle) {
      _handleRef = handle;
      bucket = (await handle.store.get(STORE_KEY)) || null;
      const liveBootID = await resolveBootID();

      if (!liveBootID) {
        // Couldn't resolve — leave bucket as-is, render what we have.
        if (!bucket) bucket = freshBucket('');
      } else if (!bucket || bucket.bootId !== liveBootID) {
        bucket = freshBucket(liveBootID);
        await handle.store.set(STORE_KEY, bucket);
      }

      // Expose to views via globals (classic-script convention used by
      // dejavu et al.). Views read `window.SessionTracker.state()`.
      window.SessionTracker = {
        state: () => bucket,
        save:  () => scheduleSave(handle),
        perMatch: () => perMatch,
        resetPerMatch: () => { perMatch = newPerMatch(); },
      };

      mountView();
    },
  });
})();
