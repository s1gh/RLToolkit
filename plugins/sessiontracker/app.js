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
    return {
      highlights: [],
      liveDurationSec: 0,
      myStats: { goals: 0, assists: 0, saves: 0, shots: 0, demos: 0, score: 0 },
      myTeam: null,
      mvp: false,
    };
  }

  // In-memory cache of the bucket. All writes go through scheduleSave().
  let bucket = null;
  let saveTimer = null;

  function scheduleSave() {
    if (saveTimer) return;
    saveTimer = setTimeout(() => {
      saveTimer = null;
      RLT.store.set(STORE_KEY, bucket);
    }, 50);
  }

  function snapshotMyTeam() {
    const me = RLT.match.current && RLT.match.current.me;
    if (me && (me.team === 0 || me.team === 1)) {
      perMatch.myTeam = me.team;
    }
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
    async ready() {
      bucket = (await RLT.store.get(STORE_KEY)) || null;
      const liveBootID = await resolveBootID();

      if (!liveBootID) {
        if (!bucket) bucket = freshBucket('');
      } else if (!bucket || bucket.bootId !== liveBootID) {
        bucket = freshBucket(liveBootID);
        await RLT.store.set(STORE_KEY, bucket);
      }

      window.SessionTracker = {
        state: () => bucket,
        save:  scheduleSave,
        perMatch: () => perMatch,
        resetPerMatch: () => { perMatch = newPerMatch(); },
      };

      mountView();
    },

    events: {
      _MatchState(p) {
        // Snapshot myTeam whenever match.current is fresh.
        snapshotMyTeam();
        // Accumulate live-phase duration for the current match.
        // _MatchState fires on every transition; previousPhase ===
        // phase only on the connect-time initial snapshot, where
        // phaseDurationSeconds is 0 and the math is a no-op.
        if (p.previousPhase === 'live' && typeof p.phaseDurationSeconds === 'number') {
          perMatch.liveDurationSec += p.phaseDurationSeconds;
        }
        // Reset per-match accumulators on the start of a fresh
        // match (lobby) or when the toolkit drops out of any match
        // (none — MatchDestroyed / connection lost / watchdog).
        if (p.phase === 'lobby' || p.phase === 'none') {
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
        scheduleSave();
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

      _Statfeed(p) {
        // MVP rides through statfeed and arrives at podium time —
        // earlier than the old _MatchSummary settle window. Use it
        // both for the per-match flag and for the highlight list.
        if (p.eventName === 'MVP' && p.mainTarget && p.mainTarget.isMe) {
          perMatch.mvp = true;
          perMatch.highlights.push('mvp');
        }
      },

      _PlayerDemolished(p) {
        if (!bucket) return;
        if (p.attacker && p.attacker.isMe) {
          bucket.totals.demosGiven++;
          bucket.totals.demos++;
          perMatch.myStats.demos++;
        }
        if (p.victim && p.victim.isMe) bucket.totals.demosReceived++;
        scheduleSave();
        if (window._sessionTrackerRender) window._sessionTrackerRender();
      },

      _OwnGoal(p) {
        if (!bucket) return;
        if (p.deflector && p.deflector.isMe) bucket.totals.ownGoals++;
        scheduleSave();
      },

      _PlayerScoreChanged(p) {
        if (!p.player || !p.player.isMe || !bucket) return;
        snapshotMyTeam();
        const d = p.delta || {};
        const t = bucket.totals;
        const ms = perMatch.myStats;
        if (typeof d.goals   === 'number') { t.goals   += d.goals;   ms.goals   += d.goals; }
        if (typeof d.assists === 'number') { t.assists += d.assists; ms.assists += d.assists; }
        if (typeof d.saves   === 'number') { t.saves   += d.saves;   ms.saves   += d.saves; }
        if (typeof d.shots   === 'number') { t.shots   += d.shots;   ms.shots   += d.shots; }
        if (typeof d.score   === 'number') { ms.score  += d.score; }
        scheduleSave();
        if (window._sessionTrackerRender) window._sessionTrackerRender();
      },

      _MatchEnded(p) {
        if (!bucket) return;
        snapshotMyTeam();
        const view = RLT.match.current || { arena: '', raw: {} };
        const rec = window.SessionTrackerState.buildMatchRecord({
          matchEnded: p,
          matchView:  view,
          myTeam:     perMatch.myTeam,
          myStats:    perMatch.myStats,
          mvp:        perMatch.mvp,
          accum: {
            durationSec: perMatch.liveDurationSec,
            highlights:  perMatch.highlights,
            endedAt:     new Date().toISOString(),
          },
        });
        if (!rec) {
          // Couldn't determine my team this match (no claimed
          // identity, or roster never resolved). Show a one-time
          // toast hinting at the most likely cause.
          if (!window._sessionTrackerNagged) {
            window._sessionTrackerNagged = true;
            if (RLT.ui && RLT.ui.toast) {
              const msg = (RLT.me && RLT.me.id)
                ? "Session tracker couldn't read this match — try playing another round."
                : 'Session tracker: claim your identity in Déjà Vu to enable tracking';
              RLT.ui.toast(msg, 4000);
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
        scheduleSave();
        if (window._sessionTrackerRender) window._sessionTrackerRender();
      },
    },
  });
})();
