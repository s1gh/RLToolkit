// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (window) {
  function computeStreaks(matches) {
    if (!matches || matches.length === 0) {
      return { current: null, best: null };
    }
    // current = run ending at the most recent match
    const lastKind = matches[matches.length - 1].result;
    let current = { kind: lastKind, count: 1 };
    for (let i = matches.length - 2; i >= 0; i--) {
      if (matches[i].result === current.kind) current.count++;
      else break;
    }
    // best = longest run anywhere
    let best = { ...current };
    let run = { kind: matches[0].result, count: 1 };
    for (let i = 1; i < matches.length; i++) {
      if (matches[i].result === run.kind) {
        run.count++;
      } else {
        run = { kind: matches[i].result, count: 1 };
      }
      if (run.count > best.count) best = { ...run };
    }
    return { current, best };
  }

  function recomputeTotals(matches) {
    const t = {
      wins: 0, losses: 0,
      goals: 0, assists: 0, saves: 0, shots: 0, demos: 0,
      mvps: 0,
      timeInMatchesSec: 0,
      currentStreak: null, bestStreak: null,
      hatTricks: 0, aerialGoals: 0, bicycleGoals: 0,
      epicSaves: 0, flipResets: 0, otGoals: 0,
      fastestGoalSec: null, hardestShotKmh: null,
      demosGiven: 0, demosReceived: 0, ownGoals: 0,
    };
    for (const m of matches) {
      if (m.result === 'win') t.wins++;
      else if (m.result === 'loss') t.losses++;
      const s = m.myStats || {};
      t.goals   += s.goals   || 0;
      t.assists += s.assists || 0;
      t.saves   += s.saves   || 0;
      t.shots   += s.shots   || 0;
      t.demos   += s.demos   || 0;
      if (m.mvp) t.mvps++;
      t.timeInMatchesSec += m.durationSec || 0;
      const h = m.highlights || [];
      if (h.includes('hatTrick'))     t.hatTricks++;
      if (h.includes('aerialGoal'))   t.aerialGoals++;
      if (h.includes('bicycleGoal'))  t.bicycleGoals++;
      if (h.includes('epicSave'))     t.epicSaves++;
      if (h.includes('flipReset'))    t.flipResets++;
      if (h.includes('otGoal'))       t.otGoals++;
    }
    const streaks = computeStreaks(matches);
    t.currentStreak = streaks.current;
    t.bestStreak    = streaks.best;
    return t;
  }

  function buildMatchRecord({ summary, matchView, myTeam, accum }) {
    if (myTeam !== 0 && myTeam !== 1) return null;
    const myEntry = (summary.players || []).find((e) => e.player && e.player.isMe);
    const myStats = myEntry
      ? {
          goals:   myEntry.goals   || 0,
          assists: myEntry.assists || 0,
          saves:   myEntry.saves   || 0,
          shots:   myEntry.shots   || 0,
          demos:   myEntry.demos   || 0,
          score:   myEntry.score   || 0,
        }
      : { goals: 0, assists: 0, saves: 0, shots: 0, demos: 0, score: 0 };
    const result = summary.winnerTeamNum === myTeam ? 'win' : 'loss';
    const scoreFor     = myTeam === 0 ? (summary.scoreBlue   || 0) : (summary.scoreOrange || 0);
    const scoreAgainst = myTeam === 0 ? (summary.scoreOrange || 0) : (summary.scoreBlue   || 0);
    return {
      endedAt:      accum.endedAt,
      result,
      scoreFor, scoreAgainst,
      durationSec:  accum.durationSec || 0,
      arena:        matchView.arena || '',
      playlist:     extractPlaylist(matchView.raw) || null,
      mvp:          !!(summary.mvp && summary.mvp.isMe),
      myStats,
      highlights:   (accum.highlights || []).slice(),
    };
  }

  function extractPlaylist(raw) {
    if (!raw) return null;
    if (typeof raw.Playlist === 'string') return raw.Playlist;
    if (raw.Game && typeof raw.Game.Playlist === 'string') return raw.Game.Playlist;
    return null;
  }

  window.SessionTrackerState = { computeStreaks, recomputeTotals, buildMatchRecord };
})(typeof window !== 'undefined' ? window : globalThis);
