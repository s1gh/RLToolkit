// Picks the single "standout" returner from a roster — the one player who
// earns the full gold treatment in the live-match views. Pure function:
// pass in the players you've already filtered (typically minus self and
// bots).
//
// The plain "DÉJÀ VU" tag is independent of this — every other player
// with encounterCount > 1 gets it. Only the standout earns the chrome.
window.DV = window.DV || {};

DV.classifyReturners = (function () {
  const { STANDOUT_RATIO, STANDOUT_ABSOLUTE } = DV.constants;

  // Returns { standoutId } — the id of the standout, or null when no one
  // qualifies. Three cases produce no standout:
  //   1. No one in the roster has been seen before (count > 1).
  //   2. There is exactly one returner whose count is too low to be
  //      remarkable on its own (< STANDOUT_ABSOLUTE).
  //   3. The field is bunched: the top returner doesn't sufficiently
  //      lead the runner-up. The whole point of the visual highlight
  //      is "this person stands out" — so when nobody does, nothing
  //      lights up.
  return function classifyReturners(players) {
    const candidates = players
      .filter((p) => p.encounterCount > 1)
      .sort((a, b) => b.encounterCount - a.encounterCount);

    if (candidates.length === 0) return { standoutId: null };

    const top = candidates[0];

    // Single returner: highlight only when their count is high enough on
    // its own merits. (Meeting one player twice in a session is not a
    // recognition moment; meeting them five+ times is.)
    if (candidates.length === 1) {
      return top.encounterCount >= STANDOUT_ABSOLUTE
        ? { standoutId: top.id }
        : { standoutId: null };
    }

    // Multiple returners: the top must clearly lead the runner-up. The
    // absolute threshold is intentionally NOT a free pass here — when
    // everyone in the lobby has played you many times, no one is "the"
    // standout, and the visual should reflect that.
    const runnerUp = candidates[1];
    const standsOut = top.encounterCount >= runnerUp.encounterCount * STANDOUT_RATIO;
    return standsOut ? { standoutId: top.id } : { standoutId: null };
  };
})();
