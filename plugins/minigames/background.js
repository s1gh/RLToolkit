// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  // Pure data layer. Testable without the SDK or DOM.

  const LEVEL_CAP = 30;

  function xpToNext(level) {
    // 100 + (L - 1) * 50. Level 1 to 2 = 100, 2 to 3 = 150, ...
    return 100 + Math.max(0, level - 1) * 50;
  }

  function emptySession(bootId) {
    return {
      bootId: bootId || '',
      level: 1,
      xpInLevel: 0,
      xpToNext: xpToNext(1),
      matches: [],
      activeChallenge: null,
      currentStreak: 0,
    };
  }

  function emptyRecords() {
    return {
      highestLevel: 1,
      longestStreak: 0,
      bestMatchXp: 0,
      firstSeenAt: Date.now(),
    };
  }

  function applyReward(session, amount) {
    if (!session || amount <= 0) return { deleveled: false };
    if (session.level >= LEVEL_CAP) {
      // Pin progress bar at full once capped.
      session.xpInLevel = session.xpToNext;
      session.currentStreak = (session.currentStreak || 0) + 1;
      return { deleveled: false };
    }
    session.xpInLevel += amount;
    session.currentStreak = (session.currentStreak || 0) + 1;
    while (session.level < LEVEL_CAP && session.xpInLevel >= session.xpToNext) {
      session.xpInLevel -= session.xpToNext;
      session.level += 1;
      session.xpToNext = xpToNext(session.level);
    }
    // Loop exited at cap with excess XP; clamp to the bar.
    if (session.level >= LEVEL_CAP) {
      session.xpInLevel = Math.min(session.xpInLevel, session.xpToNext);
    }
    return { deleveled: false };
  }

  function applyPenalty(session, amount) {
    if (!session || amount <= 0) return { deleveled: false };
    session.currentStreak = 0;
    session.xpInLevel -= amount;
    if (session.xpInLevel >= 0) return { deleveled: false };

    if (session.level <= 1) {
      session.level = 1;
      session.xpInLevel = 0;
      return { deleveled: false };
    }
    // Drop exactly one level. The "cannot drop more than one level per
    // call" rule: floor the resulting xpInLevel at 0 in the new level
    // even if the penalty would mathematically eat two levels.
    session.level -= 1;
    session.xpToNext = xpToNext(session.level);
    session.xpInLevel = session.xpToNext + session.xpInLevel; // xpInLevel is currently negative
    if (session.xpInLevel < 0) session.xpInLevel = 0;
    return { deleveled: true };
  }

  function wipeIfStaleBoot(session, currentBootId) {
    if (!session || session.bootId !== currentBootId) {
      return emptySession(currentBootId);
    }
    return session;
  }

  function freshMatchEntry(matchGuid, now, startLevel) {
    return {
      matchGuid: matchGuid || '',
      startedAt: now,
      endedAt: null,
      startLevel,
      endLevel: startLevel,
      xpGained: 0,
      completed: 0,
      failed: 0,
      timedOut: 0,
      result: null,
    };
  }

  function currentMatch(session) {
    if (!session?.matches?.length) return null;
    return session.matches[session.matches.length - 1];
  }

  function startMatch(session, { matchGuid, now }) {
    if (!session) return;
    const top = currentMatch(session);
    // Idempotency: if the same matchGuid is already the open match, do nothing.
    if (top && top.matchGuid === matchGuid && top.endedAt === null) return;
    session.matches.push(freshMatchEntry(matchGuid, now, session.level));
  }

  function recordResolution(session, { outcome, xpDelta }) {
    const m = currentMatch(session);
    // After endMatch, late-arriving resolutions must not mutate the closed recap.
    if (!m || m.endedAt !== null) return;
    if (outcome === 'completed') m.completed += 1;
    else if (outcome === 'failed') m.failed += 1;
    else if (outcome === 'timedOut') m.timedOut += 1;
    m.xpGained += xpDelta;
  }

  function endMatch(session, { matchGuid, now, result }) {
    const m = currentMatch(session);
    if (!m || m.endedAt !== null) return;
    // Tolerate either side being empty; only reject when both sides are populated and disagree.
    if (matchGuid && m.matchGuid && matchGuid !== m.matchGuid) return;
    m.endedAt = now;
    m.endLevel = session.level;
    m.result = result || null;
    session.activeChallenge = null;
  }

  function takeRecord(records, { level, streak, matchXp }) {
    if (!records) return;
    if (typeof level === 'number' && level > records.highestLevel) {
      records.highestLevel = level;
    }
    if (typeof streak === 'number' && streak > records.longestStreak) {
      records.longestStreak = streak;
    }
    if (typeof matchXp === 'number' && matchXp > records.bestMatchXp) {
      records.bestMatchXp = matchXp;
    }
  }

  root.MinigamesReducers = {
    LEVEL_CAP,
    xpToNext,
    emptySession,
    emptyRecords,
    applyReward,
    applyPenalty,
    wipeIfStaleBoot,
    startMatch,
    recordResolution,
    endMatch,
    takeRecord,
    currentMatch,
  };

  if (typeof window === 'undefined' || !window.RLT) return;
  // SDK-glue wiring added in later tasks.
})(typeof window === 'undefined' ? globalThis : window);
