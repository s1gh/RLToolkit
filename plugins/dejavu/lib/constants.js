// Shared constants for the Déjà Vu views.
window.DV = window.DV || {};

DV.constants = {
  // The "DÉJÀ VU" tag applies to every other player with encounterCount > 1.
  // The full gold treatment (border + glow + pulse) is reserved for one
  // standout per match: the single most-met opponent who clearly stands out
  // from the rest of the roster. Either threshold below qualifies.
  //
  //   STANDOUT_RATIO    — top count ≥ this × runner-up's count. 1.5 means a
  //                       6-vs-4 split qualifies, a 6-vs-5 split doesn't.
  //   STANDOUT_ABSOLUTE — fires regardless of runner-up: any returner with
  //                       count ≥ this gets the standout treatment.
  STANDOUT_RATIO: 1.5,
  STANDOUT_ABSOLUTE: 5,

  // Top-N rows in the all-time leaderboard that get the gold left-bar.
  // Distinct concept from the match-view standout above: leaderboard
  // highlights are "you keep running into these people overall," match
  // highlight is "the most familiar face *in this lobby*."
  LEADERBOARD_HIGHLIGHT_N: 5,
  LEADERBOARD_TOP_N: 10,
};
