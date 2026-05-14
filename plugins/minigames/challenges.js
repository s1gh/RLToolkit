// biome-ignore lint/suspicious/noRedundantUseStrict: classic script
'use strict';

(function (root) {
  // Tier defaults. reward = XP on completion, failPenalty = XP cost on
  // fail/timeout, timeLimitMs = default timer when an entry omits one.
  // Curated entries may override timer per-entry; reward and penalty stay
  // tied to tier so the economy is predictable.
  const tierDefaults = {
    easy:   { reward: 40,  failPenalty: 20, timeLimitMs: 45000 },
    medium: { reward: 90,  failPenalty: 45, timeLimitMs: 75000 },
    hard:   { reward: 180, failPenalty: 90, timeLimitMs: 120000 },
  };

  // Draw weight anchors per (bias, level). Levels in between are linearly
  // interpolated. Below level 1 clamps to row[0], above level 20 clamps to
  // row[3].
  const ANCHOR_LEVELS = [1, 5, 10, 20];

  const weightAnchors = {
    eased: {
      easy:   [0.80, 0.55, 0.35, 0.20],
      medium: [0.20, 0.40, 0.50, 0.55],
      hard:   [0.00, 0.05, 0.15, 0.25],
    },
    standard: {
      easy:   [0.60, 0.40, 0.25, 0.15],
      medium: [0.30, 0.40, 0.45, 0.45],
      hard:   [0.10, 0.20, 0.30, 0.40],
    },
    sharp: {
      easy:   [0.30, 0.20, 0.10, 0.05],
      medium: [0.40, 0.40, 0.35, 0.30],
      hard:   [0.30, 0.40, 0.55, 0.65],
    },
  };

  function lerp(a, b, t) {
    return a + (b - a) * t;
  }

  function interpolateAt(arr, level) {
    if (level <= ANCHOR_LEVELS[0]) return arr[0];
    if (level >= ANCHOR_LEVELS[ANCHOR_LEVELS.length - 1]) return arr[arr.length - 1];
    for (let i = 0; i < ANCHOR_LEVELS.length - 1; i += 1) {
      const lo = ANCHOR_LEVELS[i];
      const hi = ANCHOR_LEVELS[i + 1];
      if (level >= lo && level <= hi) {
        const t = (level - lo) / (hi - lo);
        return lerp(arr[i], arr[i + 1], t);
      }
    }
    // Unreachable given the clamps above and the loop covering all anchor pairs; safety net.
    return arr[arr.length - 1];
  }

  function interpolateWeights(level, bias) {
    const table = weightAnchors[bias] || weightAnchors.standard;
    return {
      easy:   interpolateAt(table.easy, level),
      medium: interpolateAt(table.medium, level),
      hard:   interpolateAt(table.hard, level),
    };
  }

  // Weighted draw. `pool` is an array of {id, tier, ...}. Step 1 picks a tier
  // weighted by the tier weights renormalised over only the tiers present in
  // the pool. Step 2 picks uniformly from the entries within that tier. If
  // the result matches `exclude` and the pool has more than one entry, we
  // reroll once and accept the second result.
  function draw({ pool, level, bias, rng, exclude }) {
    if (!Array.isArray(pool) || pool.length === 0) return null;
    const result = drawOnce(pool, level, bias, rng);
    if (!result) return null;
    if (exclude && result.id === exclude && pool.length > 1) {
      const second = drawOnce(pool, level, bias, rng);
      return second || result;
    }
    return result;
  }

  function drawOnce(pool, level, bias, rng) {
    const tierW = interpolateWeights(level, bias);

    // Step 1: pick a tier weighted by the renormalised tier weights
    // (only tiers present in the pool count). This means an easy-only
    // pool still draws an easy challenge, rather than failing because
    // medium and hard tiers held most of the original weight mass.
    // Iterate tiers in a fixed order so the cumulative-weight pick is
    // deterministic regardless of pool insertion order.
    const TIER_ORDER = ['easy', 'medium', 'hard'];
    const tiersPresent = new Set(pool.map((c) => c.tier));
    let totalTierWeight = 0;
    for (const t of TIER_ORDER) {
      if (tiersPresent.has(t)) totalTierWeight += (tierW[t] || 0);
    }
    if (totalTierWeight <= 0) {
      // All weights zero in the filtered pool: pick uniformly.
      return pool[Math.floor((rng() || 0) * pool.length)];
    }
    const r1 = (rng() || 0) * totalTierWeight;
    let acc = 0;
    let chosenTier = null;
    for (const t of TIER_ORDER) {
      if (!tiersPresent.has(t)) continue;
      acc += (tierW[t] || 0);
      if (r1 <= acc) { chosenTier = t; break; }
    }
    if (!chosenTier) {
      // Safety net: rounding edge case where r1 == totalTierWeight exactly.
      // Fall back to the first present tier in the canonical order.
      chosenTier = TIER_ORDER.find((t) => tiersPresent.has(t));
    }

    // Step 2: pick uniformly within that tier.
    const within = pool.filter((c) => c.tier === chosenTier);
    if (within.length === 0) return null;
    const r2 = Math.floor((rng() || 0) * within.length);
    // Guard against rngs that return exactly 1.0 (the contract is [0,1) but be safe).
    return within[Math.min(r2, within.length - 1)];
  }

  // ---- DSL: validation ---------------------------------------------------

  const VALID_TIERS = ['easy', 'medium', 'hard'];
  const VALID_TRIGGER_KINDS = [
    'goal', 'demo', 'save', 'epicSave', 'statfeed',
    'boostPickup', 'boostConsumed', 'touch', 'crossbar',
    'firstBlood', 'hatTrick', 'matchEndCondition',
  ];

  function validateEntry(entry) {
    const errs = [];
    if (!entry || typeof entry !== 'object') return ['entry must be an object'];
    if (typeof entry.id !== 'string' || entry.id.length === 0) errs.push('id must be a non-empty string');
    if (typeof entry.title !== 'string' || entry.title.length === 0) errs.push('title must be a non-empty string');
    if (!VALID_TIERS.includes(entry.tier)) errs.push('tier must be one of ' + VALID_TIERS.join('/'));
    if (!entry.trigger || typeof entry.trigger !== 'object') errs.push('trigger must be an object');
    else if (!VALID_TRIGGER_KINDS.includes(entry.trigger.kind)) errs.push('trigger.kind unknown: ' + entry.trigger.kind);
    if (entry.count != null && (typeof entry.count !== 'number' || entry.count < 1)) errs.push('count must be a positive integer');
    if (entry.reward != null && typeof entry.reward !== 'number') errs.push('reward must be a number');
    if (entry.failPenalty != null && typeof entry.failPenalty !== 'number') errs.push('failPenalty must be a number');
    if (entry.timeLimitMs !== undefined && entry.timeLimitMs !== null) {
      if (typeof entry.timeLimitMs !== 'number' || entry.timeLimitMs < 1000) {
        errs.push('timeLimitMs must be null or a number >= 1000');
      }
    }
    return errs;
  }

  // ---- DSL: instantiation ------------------------------------------------
  //
  // instantiate(entry, ctx) -> runtime challenge object OR null when the entry
  // can't be drawn in the current match (e.g. no opponents for a targetOpponent
  // challenge).
  //
  // The ctx contract (provided by background.js at draw time):
  //   on(eventName, handler) -> unsub        // subscribe via RLT.on
  //   matchState() -> phase string
  //   myTeam() -> 0 | 1 | null
  //   opponents() -> [{id, name, team}, ...]
  //   stats: { SAVE, SAVIOR, PLAYMAKER, MVP, ... }   // RLT.stats registry
  //   now() -> ms-epoch
  //   onComplete()                            // signal resolution as completed
  //   onFail()                                // signal resolution as failed
  //   rng() -> [0, 1)                         // for opponent picking (and reroll determinism in tests)

  function instantiate(entry, ctx) {
    const errs = validateEntry(entry);
    if (errs.length > 0) throw new Error('invalid challenge entry "' + (entry?.id) + '": ' + errs.join('; '));

    const tier = entry.tier;
    const td = tierDefaults[tier];

    // 1. Resolve a target opponent if the entry requests one.
    const opponentBindings = collectOpponentBindings(entry);
    let boundOpponent = null;
    if (opponentBindings.length > 0) {
      const opponents = ctx.opponents ? ctx.opponents() : [];
      if (!Array.isArray(opponents) || opponents.length === 0) return null;
      const idx = Math.floor((ctx.rng ? ctx.rng() : Math.random()) * opponents.length);
      boundOpponent = opponents[Math.min(idx, opponents.length - 1)];
    }

    // 2. Title template substitution.
    const title = boundOpponent
      ? entry.title.replace(/\{opponent\}/g, boundOpponent.name || 'them')
      : entry.title;

    // progressTarget surfaces multi-step challenges to the renderer.
    // boostConsumed uses sumDelta (not count) so its target is the sum.
    // Single-step (count == 1, no sumDelta) entries set null so the
    // renderer can omit the progress label entirely.
    const isBoostSum = entry.trigger?.kind === 'boostConsumed'
      && typeof entry.trigger?.filters?.sumDelta === 'number';
    const countTarget = entry.count && entry.count > 1 ? entry.count : null;
    const progressTarget = isBoostSum ? entry.trigger.filters.sumDelta : countTarget;

    const runtime = {
      id: entry.id,
      title,
      tier,
      reward: typeof entry.reward === 'number' ? entry.reward : td.reward,
      failPenalty: typeof entry.failPenalty === 'number' ? entry.failPenalty : td.failPenalty,
      // Explicit null = open-ended (no per-challenge timer). Used by
      // matchEndCondition challenges that resolve at match end.
      timeLimitMs: entry.timeLimitMs === null
        ? null
        : (typeof entry.timeLimitMs === 'number' ? entry.timeLimitMs : td.timeLimitMs),
      progressTarget,
      boundOpponent: boundOpponent ? { id: boundOpponent.id, name: boundOpponent.name } : null,
      arm: null,
    };

    // 3. Build the arm() based on trigger.kind.
    runtime.arm = buildArmer(entry, runtime, ctx);
    return runtime;
  }

  function collectOpponentBindings(entry) {
    const out = [];
    const f = entry.trigger?.filters;
    if (!f) return out;
    if (f.assister?.targetOpponent) out.push('assister');
    if (f.victimTargetOpponent) out.push('victim');
    return out;
  }

  // ---- DSL: armers per trigger kind --------------------------------------

  function buildArmer(entry, runtime, ctx) {
    const filters = entry.trigger?.filters || {};
    const target = entry.count && entry.count > 1 ? entry.count : 1;

    return function arm() {
      const unsubs = [];
      let progress = 0;
      // Once we've signalled completion or failure, ignore further events.
      // The caller (Task 6) should dispose the arm on resolution, but the
      // latch defends against late events arriving in the same tick.
      let resolved = false;
      const complete = () => { if (!resolved) { resolved = true; ctx.onComplete(); } };
      const fail = () => { if (!resolved) { resolved = true; ctx.onFail(); } };

      // Roster-loss fail-fast for bound-opponent challenges.
      if (runtime.boundOpponent) {
        unsubs.push(ctx.on('_RosterChanged', (e) => {
          if (!e || !Array.isArray(e.players)) return;
          if (!e.players.some((p) => p.id === runtime.boundOpponent.id)) {
            fail();
          }
        }));
      }

      const tick = (delta) => {
        if (resolved) return;
        progress += (typeof delta === 'number' ? delta : 1);
        // Notify only for multi-step challenges; single-step (target == 1)
        // resolves immediately on the first increment via complete().
        if (target > 1 && ctx.onProgress) ctx.onProgress(Math.min(progress, target), target);
        if (progress >= target) complete();
      };

      const kind = entry.trigger.kind;

      if (kind === 'goal') {
        unsubs.push(ctx.on('_GoalScored', (e) => {
          if (!matchGoal(e, filters, runtime)) return;
          tick();
        }));
      } else if (kind === 'demo') {
        unsubs.push(ctx.on('_PlayerDemolished', (e) => {
          if (!matchDemo(e, filters, runtime)) return;
          tick();
        }));
      } else if (kind === 'save') {
        unsubs.push(ctx.on('_StatfeedEvent', (e) => {
          const want = ctx.stats?.SAVE || 'Save';
          if (!e || e.eventName !== want) return;
          if (filters.isMe !== false && !e.mainTarget?.isMe) return;
          tick();
        }));
      } else if (kind === 'epicSave') {
        unsubs.push(ctx.on('_EpicSave', (e) => {
          if (filters.isMe !== false && !e?.mainTarget?.isMe) return;
          tick();
        }));
      } else if (kind === 'statfeed') {
        unsubs.push(ctx.on('_StatfeedEvent', (e) => {
          const want = ctx.stats?.[filters.eventName] || filters.eventName;
          if (!e || e.eventName !== want) return;
          if (filters.targetIsMe !== false && !e.mainTarget?.isMe) return;
          tick();
        }));
      } else if (kind === 'boostPickup') {
        unsubs.push(ctx.on('_BoostPickup', (e) => {
          if (!e?.player) return;
          if (filters.isMe !== false && !e.player.isMe) return;
          if (typeof filters.minDelta === 'number' && (e.delta || 0) < filters.minDelta) return;
          // Big pads always fill boost to 100 regardless of starting amount,
          // so boostAfter === 100 is the reliable big-pad detector. delta
          // is unreliable: starting near full boost yields a tiny delta
          // even when the pad picked up was a big one.
          if (typeof filters.boostAfterEq === 'number' && (e.boostAfter || 0) !== filters.boostAfterEq) return;
          tick();
        }));
      } else if (kind === 'boostConsumed') {
        // boostConsumed is sum-based, not count-based: ignore `count` and instead
        // accumulate `delta` until `filters.sumDelta` is reached.
        const goal = typeof filters.sumDelta === 'number' && filters.sumDelta > 0 ? filters.sumDelta : 0;
        let sum = 0;
        unsubs.push(ctx.on('_BoostConsumed', (e) => {
          if (!e?.player) return;
          if (filters.isMe !== false && !e.player.isMe) return;
          sum += (e.delta || 0);
          if (ctx.onProgress) ctx.onProgress(Math.min(sum, goal), goal);
          if (sum >= goal) complete();
        }));
      } else if (kind === 'touch') {
        const consecutive = filters.consecutive === true;
        // isMe filter: default true. If true, only count my touches and
        // (when consecutive) reset on any other touch. If false, count
        // every ball touch regardless of player.
        const meOnly = filters.isMe !== false;
        let streak = 0;
        unsubs.push(ctx.on('_BallHit', (e) => {
          // _BallHit ships an array of primary touchers (typically one);
          // players[0] is the resolved EnrichedPlayer for the touch.
          const player = e?.players?.[0];
          if (!player) return;
          const counts = !meOnly || player.isMe;
          if (!counts) {
            if (consecutive) streak = 0;
            return;
          }
          streak += 1;
          if (consecutive) {
            if (target > 1 && ctx.onProgress) ctx.onProgress(Math.min(streak, target), target);
            if (streak >= target) complete();
          } else {
            tick();
          }
        }));
      } else if (kind === 'crossbar') {
        unsubs.push(ctx.on('_CrossbarHit', (e) => {
          // _CrossbarHit reports the last ball-toucher under
          // ballLastTouch.player; there is no top-level player field.
          const player = e?.ballLastTouch?.player;
          if (!player) return;
          if (filters.isMe !== false && !player.isMe) return;
          tick();
        }));
      } else if (kind === 'firstBlood') {
        unsubs.push(ctx.on('_FirstBlood', (e) => {
          if (!e?.scorer) return;
          if (filters.isMe !== false && !e.scorer.isMe) return;
          tick();
        }));
      } else if (kind === 'hatTrick') {
        unsubs.push(ctx.on('_HatTrick', (e) => {
          if (!e?.mainTarget) return;
          if (filters.isMe !== false && !e.mainTarget.isMe) return;
          tick();
        }));
      } else if (kind === 'matchEndCondition') {
        // Composite predicates resolved at match end.
        if (filters.noOwnGoal) {
          let dirty = false;
          unsubs.push(ctx.on('_OwnGoal', (e) => {
            if (e?.deflector?.isMe) dirty = true;
          }));
          unsubs.push(ctx.on('_MatchEnded', () => {
            if (!dirty) complete();
          }));
        }
        if (filters.cleanRound) {
          let inRound = false; let dirty = false;
          unsubs.push(ctx.on('_MatchState', (e) => {
            if (!e) return;
            if (e.phase === 'live' && e.previousPhase !== 'live') {
              inRound = true; dirty = false;
            } else if (e.phase !== 'live' && inRound) {
              inRound = false;
              if (!dirty) complete();
            }
          }));
          unsubs.push(ctx.on('_GoalScored', (e) => {
            if (!inRound || !e) return;
            const my = ctx.myTeam ? ctx.myTeam() : null;
            if (my !== 0 && my !== 1) return;
            if (e.concedingTeam === my) dirty = true;
          }));
        }
      }

      return () => { for (const u of unsubs) { try { u(); } catch (_) {} } };
    };
  }

  function matchGoal(e, filters, runtime) {
    if (!e?.scorer) return false;
    if (filters.isMe !== false && !e.scorer.isMe) return false;
    if (filters.ownGoal === false && e.isOwnGoal) return false;
    if (filters.ownGoal === true && !e.isOwnGoal) return false;
    if (filters.modifiers) {
      const want = filters.modifiers;
      const got = e.modifiers || {};
      for (const k of Object.keys(want)) {
        const targetFlag = 'is' + k.charAt(0).toUpperCase() + k.slice(1) + 'Goal';
        // Modifiers come back as e.modifiers.isAerialGoal etc. The DSL uses
        // the short form ("aerial"), so map: aerial -> isAerialGoal.
        const flagValue = got[targetFlag] != null ? got[targetFlag] : got[k];
        if (want[k] === true && !flagValue) return false;
        if (want[k] === false && flagValue) return false;
      }
    }
    if (typeof filters.goalSpeedMin === 'number' && !(typeof e.goalSpeed === 'number' && e.goalSpeed >= filters.goalSpeedMin)) return false;
    if (filters.assister) {
      if (filters.assister.isMe === true && !e.assister?.isMe) return false;
      if (filters.assister.targetOpponent === true && runtime.boundOpponent) {
        if (!e.assister || e.assister.id !== runtime.boundOpponent.id) return false;
      }
    }
    return true;
  }

  function matchDemo(e, filters, runtime) {
    if (!e?.attacker) return false;
    if (filters.attackerIsMe !== false && !e.attacker.isMe) return false;
    if (filters.victimIsMe === true && !e.victim?.isMe) return false;
    if (filters.excludeSelfDemo !== false && e.isSelfDemo) return false;
    if (filters.excludeTeamDemo !== false && e.isTeamDemo) return false;
    if (filters.attackerWasSupersonic === true && !e.attackerWasSupersonic) return false;
    if (filters.victimTargetOpponent === true && runtime.boundOpponent) {
      if (!e.victim || e.victim.id !== runtime.boundOpponent.id) return false;
    }
    return true;
  }

  // ---- pool loader -------------------------------------------------------
  //
  // loadPool(rawJson) parses challenges.json (string or already-parsed object)
  // into an array of validated DSL entries. On any parse or validation error,
  // returns { entries: [], errors: [...] }. The state machine treats an empty
  // pool by showing "Waiting for next match", so the plugin does not crash on
  // a malformed JSON, just logs and stops drawing.
  function loadPool(raw) {
    let data;
    try {
      data = typeof raw === 'string' ? JSON.parse(raw) : raw;
    } catch (err) {
      return { entries: [], errors: ['parse error: ' + (err?.message)] };
    }
    if (!data || !Array.isArray(data.challenges)) {
      return { entries: [], errors: ['expected { challenges: [...] }'] };
    }
    const entries = [];
    const errors = [];
    for (const entry of data.challenges) {
      const errs = validateEntry(entry);
      if (errs.length > 0) {
        errors.push('challenge "' + (entry?.id) + '": ' + errs.join('; '));
        continue;
      }
      entries.push(entry);
    }
    return { entries, errors };
  }

  root.MinigamesChallenges = {
    tierDefaults,
    weightAnchors,
    interpolateWeights,
    draw,
    validateEntry,
    instantiate,
    loadPool,
  };
})(typeof window === 'undefined' ? globalThis : window);
