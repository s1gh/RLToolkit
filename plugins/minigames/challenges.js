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
    // (only tiers present in the pool count).
    const tiersPresent = {};
    for (const c of pool) tiersPresent[c.tier] = true;
    let totalTierWeight = 0;
    for (const t of Object.keys(tiersPresent)) totalTierWeight += (tierW[t] || 0);
    if (totalTierWeight <= 0) {
      // All weights zero in the filtered pool: pick uniformly.
      return pool[Math.floor((rng() || 0) * pool.length)];
    }
    const r1 = (rng() || 0) * totalTierWeight;
    let acc = 0;
    let chosenTier = null;
    for (const t of Object.keys(tiersPresent)) {
      acc += (tierW[t] || 0);
      if (r1 <= acc) { chosenTier = t; break; }
    }
    if (!chosenTier) chosenTier = Object.keys(tiersPresent)[0];

    // Step 2: pick uniformly within that tier.
    const within = pool.filter((c) => c.tier === chosenTier);
    if (within.length === 0) return null;
    const r2 = Math.floor((rng() || 0) * within.length);
    return within[Math.min(r2, within.length - 1)];
  }

  root.MinigamesChallenges = {
    tierDefaults,
    weightAnchors,
    interpolateWeights,
    draw,
  };
})(typeof window === 'undefined' ? globalThis : window);
