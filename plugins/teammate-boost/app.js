(function (root) {
  'use strict';

  function clamp(n, lo, hi) {
    if (typeof n !== 'number' || Number.isNaN(n)) return lo;
    if (n < lo) return lo;
    if (n > hi) return hi;
    return n;
  }

  const TeammateBoost = {
    clamp,
    coerceConfig() {},
    collectTeammates() {},
    isLowBoost() {},
  };

  if (root) root.TeammateBoost = TeammateBoost;
})(typeof window !== 'undefined' ? window : null);
