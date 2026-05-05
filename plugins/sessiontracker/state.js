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

  window.SessionTrackerState = { computeStreaks };
})(typeof window !== 'undefined' ? window : globalThis);
