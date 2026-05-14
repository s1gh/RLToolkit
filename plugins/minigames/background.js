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

  // ---- SDK glue ---------------------------------------------------------
  // This view is responsible for state. Other views read RLT.store and
  // re-render on change.
  //
  // Background views: the global RLT.store is read-only by SDK design;
  // only overlay/settings views can write via the global store. The
  // writable store for this plugin arrives as handle.store from the
  // register() lifecycle (with allowWrites: true), and is captured here
  // for use by every persist/notify call.

  let store = null;            // set in init(handle); handle.store is allowWrites-respecting

  const SESSION_KEY  = 'session';
  const RECORDS_KEY  = 'records';
  const SETTINGS_KEY = 'settings.difficulty';
  const TICK_KEY     = 'tick';  // bumped each timer tick so overlay can rerender

  const TICK_INTERVAL_MS = 250;  // timer poll rate; tick events emit at 1Hz, see onTick
  const DRAW_RETRY_LIMIT = 5;    // max ineligible-entry retries per drawNext call
  const CELEBRATE_MS    = 1000;  // hold the just-resolved card visible before drawing next

  let session  = null;
  let records  = null;
  let settings = { difficulty: 'standard' };

  // Timer engine state. Stored in-memory only; never written to disk.
  let activeChallenge = null;    // the instantiated runtime challenge object
  let armDispose = null;         // dispose fn returned by arm()
  let activeDeadline = null;     // ms-epoch when timer expires, or null for open-ended challenges
  let activePausedRemaining = null; // ms remaining if paused (null for open-ended)
  let tickInterval = null;
  let lastTickSecond = -1;       // last whole second of remaining time we emitted a tick for

  // Pool of validated DSL entries (NOT instantiated runtime challenges).
  // Loaded once at startup from challenges.json; instantiate() is called at
  // draw time, fresh, so each draw can resolve targetOpponent independently.
  let poolEntries = [];

  // ---- bootstrap --------------------------------------------------------

  async function bootstrap() {
    // 1. Load challenges.json.
    try {
      const resp = await fetch('challenges.json', { cache: 'no-cache' });
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      const text = await resp.text();
      const { entries, errors } = root.MinigamesChallenges.loadPool(text);
      if (errors.length) console.warn('[minigames] challenges.json had errors:', errors);
      poolEntries = entries;
      console.log('[minigames] loaded', entries.length, 'challenges');
    } catch (err) {
      console.error('[minigames] failed to load challenges.json:', err);
      poolEntries = [];
    }

    // 2. Session + records + settings.
    const rawSession = await store.get(SESSION_KEY);
    session = rawSession
      ? root.MinigamesReducers.wipeIfStaleBoot(rawSession, RLT.bootId || '')
      : root.MinigamesReducers.emptySession(RLT.bootId || '');
    if (session !== rawSession) await store.set(SESSION_KEY, session);

    const rawRecords = await store.get(RECORDS_KEY);
    records = rawRecords || root.MinigamesReducers.emptyRecords();
    if (!rawRecords) await store.set(RECORDS_KEY, records);

    const rawSettings = await store.get(SETTINGS_KEY);
    if (rawSettings?.difficulty) settings = rawSettings;
    if (!rawSettings) await store.set(SETTINGS_KEY, settings);

    // If we booted mid-match (rare: toolkit started while a match is live),
    // draw immediately so we don't waste the round.
    if (RLT.match?.state && isGameplay(RLT.match.state.phase)) {
      ensureMatchEntry();
      drawNext();
    }
  }

  function opponentsFromRoster() {
    if (!RLT.match?.current) return [];
    const my = myTeamNum();
    const roster = RLT.match.current.players || [];
    return roster
      .filter((p) => p && typeof p.team === 'number' && p.team !== my && !p.isMe)
      .map((p) => ({ id: p.id, name: p.name, team: p.team }));
  }

  function isGameplay(phase) {
    return phase === 'countdown' || phase === 'live' || phase === 'replay' || phase === 'paused';
  }

  function isLiveOrReplay(phase) {
    // The timer runs in 'live' and 'replay'; it pauses in 'paused' and 'countdown'.
    return phase === 'live' || phase === 'replay';
  }

  // ---- match lifecycle --------------------------------------------------

  function currentMatchGuid() {
    return RLT.match?.current?.matchGuid || '';
  }

  function myTeamNum() {
    const me = RLT.match?.current?.me;
    if (!me) return null;
    return typeof me.team === 'number' ? me.team : null;
  }

  function ensureMatchEntry(guidHint) {
    // Prefer the explicit guid passed by _MatchStarted, falling back to the
    // SDK's current-match snapshot. RLT.match.current can be a tick or two
    // behind the _MatchStarted emission, so the event payload is the more
    // reliable source when the handler fires.
    const guid = guidHint || currentMatchGuid();
    if (!guid) return;
    root.MinigamesReducers.startMatch(session, { matchGuid: guid, now: Date.now() });
    persistSession();
  }

  // ---- drawing ----------------------------------------------------------

  let lastDrawnId = null;
  function drawNext() {
    teardownActive();
    if (poolEntries.length === 0) return;
    // Defensive: drawNext can fire from bootstrap (mid-match recovery) or
    // from resolveActive (next challenge in the same match) before
    // _MatchStarted has stamped a match entry. If RLT.match.current has
    // a guid by now, make sure the entry exists so resolutions record
    // against the right match.
    ensureMatchEntry();
    // Up to 5 attempts: the first weighted pick may not be instantiable
    // (e.g. targetOpponent with no current opponents). Skip ineligible
    // entries and try again. After 5 misses, give up for this round.
    for (let i = 0; i < DRAW_RETRY_LIMIT; i += 1) {
      const entry = root.MinigamesChallenges.draw({
        pool: poolEntries,
        level: session.level,
        bias: settings.difficulty,
        rng: Math.random,
        exclude: lastDrawnId,
      });
      if (!entry) return;
      const ctx = buildArmContext();
      const runtime = root.MinigamesChallenges.instantiate(entry, ctx);
      if (!runtime) continue; // ineligible (e.g. no opponents); try another
      lastDrawnId = entry.id;
      activeChallenge = runtime;
      // Open-ended challenges (matchEndCondition) have timeLimitMs === null.
      // The timer engine reads activeDeadline === null as "no timeout."
      activeDeadline = runtime.timeLimitMs === null
        ? null
        : Date.now() + runtime.timeLimitMs;
      activePausedRemaining = null;
      session.activeChallenge = serialise(runtime);
      armDispose = runtime.arm();
      startTicking();
      persistSession();
      return;
    }
  }

  function serialise(c) {
    return {
      id: c.id,
      title: c.title,
      tier: c.tier,
      reward: c.reward,
      failPenalty: c.failPenalty,
      timeLimitMs: c.timeLimitMs,
      startedAt: Date.now(),
      deadline: activeDeadline,
      progress: c.progressTarget ? 0 : null,
      progressTarget: c.progressTarget || null,
    };
  }

  function buildArmContext() {
    return {
      on: (eventName, handler) => {
        const unsub = RLT.on(eventName, handler);
        return typeof unsub === 'function' ? unsub : (() => RLT.off?.(eventName, handler));
      },
      matchState: () => RLT.match?.state?.phase || 'none',
      myTeam: myTeamNum,
      opponents: opponentsFromRoster,
      stats: RLT.stats || {},
      now: () => Date.now(),
      rng: Math.random,
      onComplete: () => resolveActive('completed'),
      onFail: () => resolveActive('failed'),
      onProgress: (current, target) => {
        if (!activeChallenge || !session.activeChallenge) return;
        session.activeChallenge.progress = current;
        session.activeChallenge.progressTarget = target;
        persistSession();
      },
    };
  }

  function teardownActive() {
    stopPredicate();
    activeChallenge = null;
    if (session) session.activeChallenge = null;
    lastTickSecond = -1;
    if (celebrationTimer) { clearTimeout(celebrationTimer); celebrationTimer = null; }
  }

  // ---- resolution -------------------------------------------------------

  function resolveActive(outcome) {
    if (!activeChallenge) return;
    const c = activeChallenge;
    let xpDelta = 0;
    let result;
    if (outcome === 'completed') {
      xpDelta = c.reward;
      result = root.MinigamesReducers.applyReward(session, c.reward);
    } else {
      xpDelta = -c.failPenalty;
      result = root.MinigamesReducers.applyPenalty(session, c.failPenalty);
    }
    root.MinigamesReducers.recordResolution(session, { outcome, xpDelta });

    // Track records as we go.
    root.MinigamesReducers.takeRecord(records, {
      level: session.level,
      streak: session.currentStreak,
    });
    persistRecords();

    // Notify overlay of the resolution so it can flash, then move on.
    pushTickEvent({
      type: 'resolved',
      outcome,
      xpDelta,
      deleveled: result.deleveled,
      id: c.id,
      title: c.title,
    });

    // Two-phase resolution: stop the predicate machinery (event subs, timer)
    // immediately so further game events don't bleed into the next round, but
    // keep session.activeChallenge populated with a `resolvedAt` stamp so the
    // overlay can render a "Challenge complete!" treatment for a moment. The
    // delayed drawNext then clears the marker and arms the next challenge.
    stopPredicate();
    if (session.activeChallenge) {
      session.activeChallenge.resolvedAt = Date.now();
      session.activeChallenge.resolvedOutcome = outcome;
      session.activeChallenge.resolvedXpDelta = xpDelta;
    }
    persistSession();

    // Match-end and match-abandoned paths bypass the celebrate-then-draw flow
    // because the recap card takes over the overlay surface entirely; trying
    // to celebrate AND recap fights for the same space.
    if (!(RLT.match?.state && isGameplay(RLT.match.state.phase))) {
      teardownActive();
      persistSession();
      return;
    }

    scheduleCelebrationFlush();
  }

  let celebrationTimer = null;
  function scheduleCelebrationFlush() {
    if (celebrationTimer) clearTimeout(celebrationTimer);
    const scheduledAt = Date.now();
    console.log('[minigames] celebration window opened at', scheduledAt);
    celebrationTimer = setTimeout(() => {
      console.log('[minigames] celebration window closing after', Date.now() - scheduledAt, 'ms');
      celebrationTimer = null;
      teardownActive();
      // Only draw the next one if we're still in a gameplay phase.
      if (RLT.match?.state && isGameplay(RLT.match.state.phase)) {
        drawNext();
      } else {
        persistSession();
      }
    }, CELEBRATE_MS);
  }

  // stopPredicate halts the event subscriptions and timer for the active
  // challenge without nulling session.activeChallenge. Used by the
  // celebrate-then-draw flow; full teardown happens in teardownActive.
  function stopPredicate() {
    if (armDispose) { try { armDispose(); } catch (_) {} armDispose = null; }
    activeDeadline = null;
    activePausedRemaining = null;
    stopTicking();
  }

  function timeoutActive() {
    if (!activeChallenge) return;
    resolveActive('timedOut');
  }

  // ---- timer ------------------------------------------------------------

  function startTicking() {
    stopTicking();
    tickInterval = setInterval(onTick, TICK_INTERVAL_MS);
  }
  function stopTicking() {
    if (tickInterval) { clearInterval(tickInterval); tickInterval = null; }
  }
  function onTick() {
    if (!activeChallenge) return;
    // Open-ended challenge: nothing to count down. Emit a single tick so
    // the overlay knows we're active, then stop bothering the store until
    // the challenge resolves.
    if (activeDeadline === null) {
      if (lastTickSecond !== -1) return;
      lastTickSecond = 0;
      pushTickEvent({ type: 'tick', remainingMs: null });
      return;
    }
    const phase = RLT.match?.state?.phase || 'none';
    if (!isLiveOrReplay(phase)) {
      // Pause: remember remaining time, freeze deadline.
      if (activePausedRemaining === null) {
        activePausedRemaining = Math.max(0, activeDeadline - Date.now());
      }
      return;
    }
    if (activePausedRemaining !== null) {
      activeDeadline = Date.now() + activePausedRemaining;
      activePausedRemaining = null;
    }
    if (Date.now() >= activeDeadline) {
      timeoutActive();
      return;
    }
    // Only push a tick when the displayed second changes. The store-backed
    // tick channel doesn't need sub-second resolution, and 4Hz writes would
    // hammer the JSON-on-disk store with no user-visible benefit.
    const remainingMs = activeDeadline - Date.now();
    const sec = Math.ceil(remainingMs / 1000);
    if (sec !== lastTickSecond) {
      lastTickSecond = sec;
      pushTickEvent({ type: 'tick', remainingMs });
    }
  }

  // ---- persistence + notification --------------------------------------

  let saveInFlight = false;
  let saveDirty = false;
  function persistSession() {
    // Coalesce concurrent writes. store.set is async and the in-flight
    // request will serialise whatever the session object holds at JSON-time;
    // if more mutations land before the write resolves, set the dirty flag
    // and re-fire once.
    if (!store) return;
    if (saveInFlight) { saveDirty = true; return; }
    saveInFlight = true;
    saveDirty = false;
    store.set(SESSION_KEY, session).then(() => {
      saveInFlight = false;
      if (saveDirty) persistSession();
    });
  }
  function persistRecords() {
    if (!store) return;
    store.set(RECORDS_KEY, records);
  }

  // Overlays read this counter via store.onChange('tick', ...) and rerender.
  // Payload is intentionally tiny: full state lives under the session key.
  let tickSeq = 0;
  function pushTickEvent(evt) {
    if (!store) return;
    tickSeq += 1;
    store.set(TICK_KEY, { seq: tickSeq, ...evt, at: Date.now() });
  }

  // ---- match end shared path -------------------------------------------

  function finishMatch(matchGuid, result) {
    // Defensive: a previous resolveActive() may have drawn a fresh challenge
    // if the phase still reported gameplay when _MatchAbandoned fired. Kill
    // any survivor before stamping the match as ended.
    teardownActive();
    root.MinigamesReducers.endMatch(session, { matchGuid, now: Date.now(), result });
    const m = root.MinigamesReducers.currentMatch(session);
    if (m) {
      root.MinigamesReducers.takeRecord(records, {
        level: session.level,
        streak: session.currentStreak,
        matchXp: m.xpGained,
      });
      persistRecords();
    }
    pushTickEvent({ type: 'matchEnded', matchGuid, result });
    persistSession();
  }

  // ---- register ---------------------------------------------------------

  RLT.plugin.register({
    // Background views are read-only by default in the SDK; the state
    // machine lives here and must write session/records/tick/settings.
    allowWrites: true,

    init(handle) {
      // The global RLT.store is read-only for background views; the
      // writable per-plugin store arrives on the handle. Capture it for
      // every persist/notify path.
      store = handle.store;
    },

    async ready() {
      await bootstrap();
    },

    events: {
      _MatchStarted(e) {
        if (!e?.matchGuid) return;
        ensureMatchEntry(e.matchGuid);
        if (!activeChallenge && isGameplay(RLT.match?.state?.phase)) {
          drawNext();
        }
      },

      _MatchEnded(e) {
        if (activeChallenge) timeoutActive();
        const guid = e?.matchGuid || currentMatchGuid();
        const winner = typeof e?.winnerTeamNum === 'number' ? e.winnerTeamNum : null;
        const my = myTeamNum();
        let result = null;
        if (winner === null) result = null;
        else if (my === null) result = null;
        else if (winner === my) result = 'win';
        else result = 'loss';
        finishMatch(guid, result);
      },

      _MatchAbandoned(e) {
        if (activeChallenge) resolveActive('failed');
        const guid = e?.matchGuid || currentMatchGuid();
        finishMatch(guid, 'forfeit');
      },
    },

    dispose() {
      stopTicking();
      if (armDispose) {
        try { armDispose(); } catch (_) {}
        armDispose = null;
      }
    },
  });
})(typeof window === 'undefined' ? globalThis : window);
