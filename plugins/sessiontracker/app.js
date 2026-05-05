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

    events: {
      _LifecyclePhaseChanged(p) {
        // Accumulate live-phase duration for the current match.
        if (p.from === 'live' && typeof p.phaseDurationSeconds === 'number') {
          perMatch.liveDurationSec += p.phaseDurationSeconds;
        }
        // Reset per-match accumulators when leaving 'podium' or hitting
        // 'lobby' (start of a fresh match).
        if (p.to === 'lobby' || p.to === 'none') {
          perMatch = newPerMatch();
        }
      },

      _GoalScored(p) {
        if (!p.scorer || !p.scorer.isMe) return;
        const m = p.modifiers || {};
        if (m.isAerialGoal)   perMatch.highlights.push('aerialGoal');
        if (m.isBicycleGoal)  perMatch.highlights.push('bicycleGoal');
        if (m.isOvertimeGoal) perMatch.highlights.push('otGoal');
        // Live-tracked totals:
        if (!bucket) return;
        const t = bucket.totals;
        if (typeof p.goalTime === 'number') {
          if (t.fastestGoalSec === null || p.goalTime < t.fastestGoalSec) {
            t.fastestGoalSec = p.goalTime;
          }
        }
        if (typeof p.goalSpeed === 'number') {
          // goalSpeed is km/h per the docs; track max.
          if (t.hardestShotKmh === null || p.goalSpeed > t.hardestShotKmh) {
            t.hardestShotKmh = p.goalSpeed;
          }
        }
        scheduleSaveExternal();
      },

      _HatTrick(p) {
        if (p.mainTarget && p.mainTarget.isMe) {
          perMatch.highlights.push('hatTrick');
        }
      },

      _EpicSave(p) {
        if (p.mainTarget && p.mainTarget.isMe) {
          perMatch.highlights.push('epicSave');
        }
      },

      _FlipReset(p) {
        if (p.mainTarget && p.mainTarget.isMe) {
          perMatch.highlights.push('flipReset');
        }
      },

      _FirstBlood(p) {
        // _FirstBlood doesn't carry a player directly; rely on the
        // correlated _GoalScored.scorer if present, otherwise on
        // RLT.match.current.me.
        const me = RLT.match.current && RLT.match.current.me;
        const scorerId = (p.correlatedGoalScorer && p.correlatedGoalScorer.id)
          || (p.scorer && p.scorer.id);
        if (me && scorerId && scorerId === me.id) {
          perMatch.highlights.push('firstBlood');
        }
      },

      _PlayerDemolished(p) {
        if (!bucket) return;
        if (p.attacker && p.attacker.isMe) bucket.totals.demosGiven++;
        if (p.victim && p.victim.isMe)     bucket.totals.demosReceived++;
        scheduleSaveExternal();
      },

      _OwnGoal(p) {
        if (!bucket) return;
        if (p.deflector && p.deflector.isMe) bucket.totals.ownGoals++;
        scheduleSaveExternal();
      },

      _MatchSummary(p) {
        if (!bucket) return;
        const view = RLT.match.current || { arena: '', raw: {} };
        const myTeam = (view.me && (view.me.team === 0 || view.me.team === 1))
          ? view.me.team : null;
        const rec = window.SessionTrackerState.buildMatchRecord({
          summary: p,
          matchView: view,
          myTeam,
          accum: {
            durationSec: perMatch.liveDurationSec,
            highlights:  perMatch.highlights,
            endedAt:     new Date().toISOString(),
          },
        });
        if (!rec) {
          // Identity not claimed — show a one-time toast.
          if (!window._sessionTrackerNagged) {
            window._sessionTrackerNagged = true;
            if (RLT.ui && RLT.ui.toast) {
              RLT.ui.toast('Session tracker: claim your identity in Déjà Vu to enable tracking', 4000);
            }
          }
          perMatch = newPerMatch();
          return;
        }
        bucket.matches.push(rec);
        // Recompute aggregates derivable from match list, then preserve
        // live-tracked fields (fastestGoalSec, hardestShotKmh, demosGiven,
        // demosReceived, ownGoals) which recomputeTotals doesn't know about.
        const live = {
          fastestGoalSec: bucket.totals.fastestGoalSec,
          hardestShotKmh: bucket.totals.hardestShotKmh,
          demosGiven:     bucket.totals.demosGiven,
          demosReceived:  bucket.totals.demosReceived,
          ownGoals:       bucket.totals.ownGoals,
        };
        bucket.totals = Object.assign(
          window.SessionTrackerState.recomputeTotals(bucket.matches),
          live,
        );
        perMatch = newPerMatch();
        scheduleSaveExternal();
        if (window._sessionTrackerRender) window._sessionTrackerRender();
      },
    },
  });
})();
