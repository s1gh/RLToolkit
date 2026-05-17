(function (root) {
  'use strict';

  // Pure helpers exported for the test suite. Filled in by later
  // tasks; the test harness only needs the keys to exist as functions
  // initially so we can drive the rest of the build TDD-style.
  const TeammateBoost = {
    clamp() {},
    coerceConfig() {},
    collectTeammates() {},
    isLowBoost() {},
  };

  if (root) root.TeammateBoost = TeammateBoost;
})(typeof window !== 'undefined' ? window : null);
