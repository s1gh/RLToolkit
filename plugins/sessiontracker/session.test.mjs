import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
const sandbox = { window: {} };
new Function('window', src)(sandbox.window);
const R = sandbox.window.SessionTrackerReducers;

test('emptyBucket: shape contains all top-level keys', () => {
  const b = R.emptyBucket('boot-1');
  assert.equal(b.bootId, 'boot-1');
  assert.equal(typeof b.startedAt, 'string');
  assert.deepEqual(b.results, { wins: 0, losses: 0, last: [] });
  assert.deepEqual(b.totals, { goals: 0, saves: 0, demos: 0, boost: 0, pickups: 0 });
  assert.equal(b.modifiers.aerial, 0);
  assert.equal(b.modifiers.poolShot, 0);
  assert.deepEqual(b.ball, { fastestKmh: null, myFastestHitKmh: null, fastestGoalKmh: null });
  assert.deepEqual(b.crossbar, { hits: 0, hardest: null });
  assert.deepEqual(b.mmr, { ranked: {}, casual: null });
});

test('emptyBucket: empty bootId stays empty string', () => {
  assert.equal(R.emptyBucket().bootId, '');
  assert.equal(R.emptyBucket(null).bootId, '');
});

test('applyMatchEnded: my team wins → wins++ and "win" pushed', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { winnerTeamNum: 0, scoreBlue: 3, scoreOrange: 1 }, 0);
  assert.equal(b.results.wins, 1);
  assert.equal(b.results.losses, 0);
  assert.deepEqual(b.results.last, ['win']);
});

test('applyMatchEnded: my team loses → losses++ and "loss" pushed', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { winnerTeamNum: 1, scoreBlue: 1, scoreOrange: 4 }, 0);
  assert.equal(b.results.wins, 0);
  assert.equal(b.results.losses, 1);
  assert.deepEqual(b.results.last, ['loss']);
});

test('applyMatchEnded: myTeam not in {0,1} → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { winnerTeamNum: 0 }, null);
  assert.equal(b.results.wins, 0);
  assert.deepEqual(b.results.last, []);
});

test('applyMatchEnded: last[] caps at 10 entries with FIFO eviction', () => {
  const b = R.emptyBucket('b');
  for (let i = 0; i < 12; i++) {
    R.applyMatchEnded(b, { winnerTeamNum: 0 }, 0);
  }
  assert.equal(b.results.last.length, 10);
  assert.equal(b.results.wins, 12);
  assert.ok(b.results.last.every((r) => r === 'win'));
});

test('applyMatchEnded: winnerTeamNum missing → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, {}, 0);
  assert.equal(b.results.wins, 0);
  assert.deepEqual(b.results.last, []);
});

test('applyPlayerScoreChanged: isMe with goals + saves delta', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: true }, delta: { goals: 2, saves: 1 } });
  assert.equal(b.totals.goals, 2);
  assert.equal(b.totals.saves, 1);
  assert.equal(b.totals.demos, 0);
});

test('applyPlayerScoreChanged: non-self → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: false }, delta: { goals: 1 } });
  assert.equal(b.totals.goals, 0);
});

test('applyPlayerScoreChanged: missing delta tolerated', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: true } });
  assert.equal(b.totals.goals, 0);
});

test('applyPlayerScoreChanged: assists/shots/score ignored', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: true }, delta: { goals: 1, assists: 1, shots: 5, score: 250 } });
  assert.equal(b.totals.goals, 1);
  assert.equal(b.totals.assists, undefined);
});

// Real match -> freeplay can produce a negative delta when the
// scoreboard goes from "Goals 3" to "Goals 0". The reducer must
// refuse to decrement a counter the user already earned, even if
// such a delta slips past the backend's match-boundary guard.
test('applyPlayerScoreChanged: negative delta does not decrement totals', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: true }, delta: { goals: 3, saves: 2 } });
  assert.equal(b.totals.goals, 3);
  assert.equal(b.totals.saves, 2);
  R.applyPlayerScoreChanged(b, { player: { isMe: true }, delta: { goals: -3, saves: -2 } });
  assert.equal(b.totals.goals, 3);
  assert.equal(b.totals.saves, 2);
  assert.equal(b.match.goals, 3);
  assert.equal(b.match.saves, 2);
});

test('applyPlayerDemolished: I am attacker → demos++', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerDemolished(b, { attacker: { isMe: true }, victim: { isMe: false } });
  assert.equal(b.totals.demos, 1);
});

test('applyPlayerDemolished: someone else attacker → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerDemolished(b, { attacker: { isMe: false }, victim: { isMe: true } });
  assert.equal(b.totals.demos, 0);
});

test('applyPlayerDemolished: missing attacker tolerated', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerDemolished(b, {});
  assert.equal(b.totals.demos, 0);
});

test('applyGoalScored: non-self goal → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, { scorer: { isMe: false }, modifiers: { isAerialGoal: true } });
  assert.equal(b.modifiers.aerial, 0);
});

test('applyGoalScored: self aerial goal → aerial++', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, { scorer: { isMe: true }, modifiers: { isAerialGoal: true } });
  assert.equal(b.modifiers.aerial, 1);
});

test('applyGoalScored: multiple flags in one goal increment each', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, {
    scorer: { isMe: true },
    modifiers: { isAerialGoal: true, isBicycleGoal: true, isLongGoal: true, isOvertimeGoal: true },
  });
  assert.equal(b.modifiers.aerial, 1);
  assert.equal(b.modifiers.bicycle, 1);
  assert.equal(b.modifiers.longGoal, 1);
  assert.equal(b.modifiers.overtime, 1);
});

test('applyGoalScored: all 9 supported flags mapped', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, {
    scorer: { isMe: true },
    modifiers: {
      isAerialGoal: true, isBicycleGoal: true, isLongGoal: true, isOvertimeGoal: true,
      isHatTrickGoal: true, isFlipResetGoal: true, isBackwardsGoal: true,
      isTurtleGoal: true, isPoolShot: true,
    },
  });
  assert.equal(b.modifiers.aerial, 1);
  assert.equal(b.modifiers.bicycle, 1);
  assert.equal(b.modifiers.longGoal, 1);
  assert.equal(b.modifiers.overtime, 1);
  assert.equal(b.modifiers.hatTrick, 1);
  assert.equal(b.modifiers.flipReset, 1);
  assert.equal(b.modifiers.backwards, 1);
  assert.equal(b.modifiers.turtle, 1);
  assert.equal(b.modifiers.poolShot, 1);
});

test('applyGoalScored: isHoopsSwishGoal is NOT tracked', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, { scorer: { isMe: true }, modifiers: { isHoopsSwishGoal: true } });
  assert.equal(b.modifiers.hoopsSwish, undefined);
});

test('applyGoalScored: missing modifiers object tolerated', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, { scorer: { isMe: true } });
  assert.equal(b.modifiers.aerial, 0);
});

test('applyCrossbarHit: my first hit seeds hits and hardest', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, {
    impactForce: 800, ballSpeed: 130,
    ballLastTouch: { player: { name: 'Me', team: 0, isMe: true } },
  });
  assert.equal(b.crossbar.hits, 1);
  assert.equal(b.crossbar.hardest.impact, 800);
  assert.equal(b.crossbar.hardest.speed, 130);
  assert.equal(b.crossbar.hardest.player.name, 'Me');
  assert.equal(b.crossbar.hardest.player.team, 0);
  assert.equal(b.crossbar.hardest.player.isMe, true);
  assert.equal(typeof b.crossbar.hardest.at, 'string');
});

test('applyCrossbarHit: opponent hit ignored entirely', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, {
    impactForce: 800, ballSpeed: 130,
    ballLastTouch: { player: { name: 'Apparition', team: 1, isMe: false } },
  });
  assert.equal(b.crossbar.hits, 0);
  assert.equal(b.crossbar.hardest, null);
});

test('applyCrossbarHit: strictly greater of mine replaces hardest', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 800, ballSpeed: 130, ballLastTouch: { player: { name: 'Me', team: 0, isMe: true } } });
  R.applyCrossbarHit(b, { impactForce: 900, ballSpeed: 140, ballLastTouch: { player: { name: 'Me', team: 0, isMe: true } } });
  assert.equal(b.crossbar.hits, 2);
  assert.equal(b.crossbar.hardest.impact, 900);
});

test('applyCrossbarHit: equal impact keeps the earlier record', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 800, ballSpeed: 130, ballLastTouch: { player: { name: 'Me', team: 0, isMe: true } } });
  R.applyCrossbarHit(b, { impactForce: 800, ballSpeed: 150, ballLastTouch: { player: { name: 'Me', team: 0, isMe: true } } });
  assert.equal(b.crossbar.hits, 2);
  assert.equal(b.crossbar.hardest.speed, 130);
});

test('applyCrossbarHit: missing impactForce → still counted, hardest unchanged', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { ballLastTouch: { player: { name: 'Me', team: 0, isMe: true } } });
  assert.equal(b.crossbar.hits, 1);
  assert.equal(b.crossbar.hardest, null);
});

test('applyMmr: first 2v2 ranked seeds start and current', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, '2v2', { playlists: { '2v2': { mmr: 1432 }, 'casual': { mmr: 1023 } } });
  assert.deepEqual(b.mmr.ranked['2v2'], { start: 1432, current: 1432 });
  assert.deepEqual(b.mmr.casual, { start: 1023, current: 1023 });
});

test('applyMmr: second call updates current only', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, '2v2', { playlists: { '2v2': { mmr: 1432 }, 'casual': { mmr: 1023 } } });
  R.applyMmr(b, '2v2', { playlists: { '2v2': { mmr: 1450 }, 'casual': { mmr: 1018 } } });
  assert.deepEqual(b.mmr.ranked['2v2'], { start: 1432, current: 1450 });
  assert.deepEqual(b.mmr.casual, { start: 1023, current: 1018 });
});

test('applyMmr: different mode opens a new ranked slot', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, '2v2', { playlists: { '2v2': { mmr: 1432 }, 'casual': { mmr: 1023 } } });
  R.applyMmr(b, '1v1', { playlists: { '1v1': { mmr: 874 }, 'casual': { mmr: 1023 } } });
  assert.deepEqual(b.mmr.ranked['2v2'], { start: 1432, current: 1432 });
  assert.deepEqual(b.mmr.ranked['1v1'], { start: 874, current: 874 });
});

test('applyMmr: missing ranked key in response leaves slot untouched', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, '2v2', { playlists: { '2v2': { mmr: 1432 } } });
  R.applyMmr(b, '2v2', { playlists: {} });
  assert.deepEqual(b.mmr.ranked['2v2'], { start: 1432, current: 1432 });
});

test('applyMmr: missing casual key leaves casual slot untouched', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, '2v2', { playlists: { '2v2': { mmr: 1432 } } });
  assert.equal(b.mmr.casual, null);
});

test('applyMmr: payload with no playlists is a no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, '2v2', {});
  assert.deepEqual(b.mmr.ranked, {});
  assert.equal(b.mmr.casual, null);
});

test('applyMmr: mode other than 1v1/2v2/3v3 does not update ranked', () => {
  const b = R.emptyBucket('b');
  R.applyMmr(b, 'other', { playlists: { 'casual': { mmr: 1000 } } });
  assert.deepEqual(b.mmr.ranked, {});
  assert.deepEqual(b.mmr.casual, { start: 1000, current: 1000 });
});

test('modeFromRoster: 2/4/6 → 1v1/2v2/3v3', () => {
  assert.equal(R.modeFromRoster(2), '1v1');
  assert.equal(R.modeFromRoster(4), '2v2');
  assert.equal(R.modeFromRoster(6), '3v3');
});

test('modeFromRoster: other sizes → "other"', () => {
  assert.equal(R.modeFromRoster(0), 'other');
  assert.equal(R.modeFromRoster(1), 'other');
  assert.equal(R.modeFromRoster(3), 'other');
  assert.equal(R.modeFromRoster(8), 'other');
});

test('currentStreak: empty → null', () => {
  assert.equal(R.currentStreak([]), null);
});

test('currentStreak: single match → null (rule: hide below 2)', () => {
  assert.equal(R.currentStreak(['win']), null);
});

test('currentStreak: WW → W2', () => {
  assert.deepEqual(R.currentStreak(['win', 'win']), { kind: 'win', count: 2 });
});

test('currentStreak: LWWW → W3', () => {
  assert.deepEqual(R.currentStreak(['loss', 'win', 'win', 'win']), { kind: 'win', count: 3 });
});

test('currentStreak: WWWLL → L2', () => {
  assert.deepEqual(R.currentStreak(['win', 'win', 'win', 'loss', 'loss']), { kind: 'loss', count: 2 });
});

test('currentStreak: alternating → 1 → null', () => {
  assert.equal(R.currentStreak(['win', 'loss', 'win', 'loss']), null);
});

// Per-match block: zeroed on bucket creation. Mirrors session.totals
// shape so the renderer can reuse the same field names; result is null
// until applyMatchEnded stamps it.
test('emptyBucket: includes match block with zeros + null result', () => {
  const b = R.emptyBucket('b');
  assert.equal(b.match.result, null);
  assert.equal(b.match.goals, 0);
  assert.equal(b.match.saves, 0);
  assert.equal(b.match.demos, 0);
  assert.equal(b.match.boost, 0);
  assert.equal(b.match.pickups, 0);
  assert.equal(b.match.modifiers.aerial, 0);
  assert.equal(b.match.modifiers.poolShot, 0);
  assert.equal(b.match.modifiers.ownGoal, 0);
});

test('resetMatch: zeros per-match counters and clears result', () => {
  const b = R.emptyBucket('b');
  b.match.result = 'win';
  b.match.goals = 3;
  b.match.saves = 2;
  b.match.demos = 1;
  b.match.boost = 40;
  b.match.pickups = 60;
  b.match.modifiers.aerial = 2;
  b.match.modifiers.ownGoal = 1;
  R.resetMatch(b);
  assert.equal(b.match.result, null);
  assert.equal(b.match.goals, 0);
  assert.equal(b.match.saves, 0);
  assert.equal(b.match.demos, 0);
  assert.equal(b.match.boost, 0);
  assert.equal(b.match.pickups, 0);
  assert.equal(b.match.modifiers.aerial, 0);
  assert.equal(b.match.modifiers.ownGoal, 0);
});

test('applyPlayerScoreChanged: bumps both session and match totals', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: true }, delta: { goals: 2, saves: 1 } });
  assert.equal(b.totals.goals, 2);
  assert.equal(b.match.goals, 2);
  assert.equal(b.totals.saves, 1);
  assert.equal(b.match.saves, 1);
});

test('applyPlayerScoreChanged: non-self leaves match block alone', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: false }, delta: { goals: 5 } });
  assert.equal(b.match.goals, 0);
});

test('applyPlayerDemolished: bumps both session and match demos', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerDemolished(b, { attacker: { isMe: true } });
  R.applyPlayerDemolished(b, { attacker: { isMe: true } });
  assert.equal(b.totals.demos, 2);
  assert.equal(b.match.demos, 2);
});

test('applyMatchEnded: records win on match block', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { winnerTeamNum: 0 }, 0);
  assert.equal(b.match.result, 'win');
});

test('applyMatchEnded: records loss on match block', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { winnerTeamNum: 1 }, 0);
  assert.equal(b.match.result, 'loss');
});

test('applyMatchEnded: ambiguous winner leaves match.result alone', () => {
  const b = R.emptyBucket('b');
  b.match.result = 'win';
  R.applyMatchEnded(b, {}, 0);
  assert.equal(b.match.result, 'win');
});

// Guid-deduped tally so the same match never counts twice. Both
// _MatchEnded (the wire event) and a phase=ended state transition can
// trigger applyMatchEnded — first write wins for a given matchGuid.
test('applyMatchEnded: same guid cannot tally twice', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { matchGuid: 'g1', winnerTeamNum: 0 }, 0);
  R.applyMatchEnded(b, { matchGuid: 'g1', winnerTeamNum: 0 }, 0);
  assert.equal(b.results.wins, 1);
  assert.equal(b.results.last.length, 1);
});

test('applyMatchEnded: different guids tally independently', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { matchGuid: 'g1', winnerTeamNum: 0 }, 0);
  R.applyMatchEnded(b, { matchGuid: 'g2', winnerTeamNum: 1 }, 0);
  assert.equal(b.results.wins, 1);
  assert.equal(b.results.losses, 1);
  assert.equal(b.results.last.length, 2);
});

// Score-derived winner: when winnerTeamNum is absent, fall back to
// whichever team scored more.
test('applyMatchEnded: derives winner from scoreBlue > scoreOrange', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { matchGuid: 'g1', scoreBlue: 3, scoreOrange: 1 }, 0);
  assert.equal(b.results.wins, 1);
  assert.equal(b.match.result, 'win');
});

test('applyMatchEnded: derives winner from scoreOrange > scoreBlue', () => {
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { matchGuid: 'g1', scoreBlue: 1, scoreOrange: 4 }, 0);
  assert.equal(b.results.losses, 1);
  assert.equal(b.match.result, 'loss');
});

test('applyMatchEnded: tied scores with no winnerTeamNum is a no-op', () => {
  // RL doesn't end matches in ties under normal play (overtime keeps
  // running), so a tie payload signals incomplete data — don't guess.
  const b = R.emptyBucket('b');
  R.applyMatchEnded(b, { matchGuid: 'g1', scoreBlue: 2, scoreOrange: 2 }, 0);
  assert.equal(b.results.wins, 0);
  assert.equal(b.results.losses, 0);
});

test('match block survives a per-match reset without nuking session totals', () => {
  const b = R.emptyBucket('b');
  R.applyPlayerScoreChanged(b, { player: { isMe: true }, delta: { goals: 3 } });
  R.applyPlayerDemolished(b, { attacker: { isMe: true } });
  R.resetMatch(b);
  assert.equal(b.totals.goals, 3, 'session goals preserved');
  assert.equal(b.totals.demos, 1, 'session demos preserved');
  assert.equal(b.match.goals, 0, 'match goals cleared');
  assert.equal(b.match.demos, 0, 'match demos cleared');
});

// FASTEST shot is a personal record, not a match-wide leaderboard
// entry. The event fires for every player's shot; we only want to
// remember our own.
// applyMyHit: from _BallHit, tracks my hardest hit (fastest ball
// speed I've imparted via touch). Filters on players[0].isMe — that's
// the toucher in the BallHit envelope.
test('applyMyHit: ignores opponent touches', () => {
  const b = R.emptyBucket('b');
  R.applyMyHit(b, { postHitSpeed: 250, players: [{ isMe: false }] });
  assert.equal(b.ball.myFastestHitKmh, null);
});

test('applyMyHit: records my touch speed', () => {
  const b = R.emptyBucket('b');
  R.applyMyHit(b, { postHitSpeed: 142.6, players: [{ isMe: true }] });
  assert.equal(b.ball.myFastestHitKmh, 142.6);
});

test('applyMyHit: keeps the highest of mine', () => {
  const b = R.emptyBucket('b');
  R.applyMyHit(b, { postHitSpeed: 100, players: [{ isMe: true }] });
  R.applyMyHit(b, { postHitSpeed: 80,  players: [{ isMe: true }] });
  R.applyMyHit(b, { postHitSpeed: 250, players: [{ isMe: true }] });
  assert.equal(b.ball.myFastestHitKmh, 250);
});

test('applyMyHit: missing players or postHitSpeed → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMyHit(b, { postHitSpeed: 200 });
  R.applyMyHit(b, { postHitSpeed: 200, players: [] });
  R.applyMyHit(b, { players: [{ isMe: true }] });
  R.applyMyHit(b, { postHitSpeed: 'fast', players: [{ isMe: true }] });
  assert.equal(b.ball.myFastestHitKmh, null);
});

// applyMatchTopSpeed: match-wide highest ball speed across all
// players. Source is _FastestShotOfMatch, which the toolkit only
// publishes when a new match max is observed — so we just take it.
test('applyMatchTopSpeed: records the wire speed value', () => {
  const b = R.emptyBucket('b');
  R.applyMatchTopSpeed(b, { speed: 200 });
  assert.equal(b.ball.fastestKmh, 200);
});

test('applyMatchTopSpeed: only ratchets up', () => {
  // The emitter already enforces this match-wide, but the reducer is
  // defensive in case of out-of-order delivery on reconnect.
  const b = R.emptyBucket('b');
  R.applyMatchTopSpeed(b, { speed: 200 });
  R.applyMatchTopSpeed(b, { speed: 150 });
  assert.equal(b.ball.fastestKmh, 200);
});

test('applyMatchTopSpeed: bad input is a no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMatchTopSpeed(b, {});
  R.applyMatchTopSpeed(b, { speed: 'fast' });
  R.applyMatchTopSpeed(b, null);
  assert.equal(b.ball.fastestKmh, null);
});

// Boost consumed: backend emits _BoostConsumed with a positive delta
// every time a player's Boost dropped between ticks (active spend,
// not respawn). Plugin sums my deltas into both session totals and
// per-match counters.
test('applyBoostConsumed: ignores opponents', () => {
  const b = R.emptyBucket('b');
  R.applyBoostConsumed(b, { delta: 25, player: { isMe: false } });
  assert.equal(b.totals.boost, 0);
  assert.equal(b.match.boost, 0);
});

test('applyBoostConsumed: sums my deltas into both totals and match', () => {
  const b = R.emptyBucket('b');
  R.applyBoostConsumed(b, { delta: 30, player: { isMe: true } });
  R.applyBoostConsumed(b, { delta: 12, player: { isMe: true } });
  assert.equal(b.totals.boost, 42);
  assert.equal(b.match.boost, 42);
});

test('applyBoostConsumed: missing or non-positive delta is a no-op', () => {
  const b = R.emptyBucket('b');
  R.applyBoostConsumed(b, { player: { isMe: true } });
  R.applyBoostConsumed(b, { delta: 0, player: { isMe: true } });
  R.applyBoostConsumed(b, { delta: -5, player: { isMe: true } });
  R.applyBoostConsumed(b, null);
  assert.equal(b.totals.boost, 0);
});

// Own goals: a special goal modifier. Counts in both session
// modifiers and the per-match modifier block when I'm the deflector.
test('applyOwnGoal: ignores opponent own goals', () => {
  const b = R.emptyBucket('b');
  R.applyOwnGoal(b, { deflector: { isMe: false } });
  assert.equal(b.modifiers.ownGoal, 0);
  assert.equal(b.match.modifiers.ownGoal, 0);
});

test('applyOwnGoal: bumps both session and match modifier slots when mine', () => {
  const b = R.emptyBucket('b');
  R.applyOwnGoal(b, { deflector: { isMe: true } });
  R.applyOwnGoal(b, { deflector: { isMe: true } });
  assert.equal(b.modifiers.ownGoal, 2);
  assert.equal(b.match.modifiers.ownGoal, 2);
});

test('applyOwnGoal: missing deflector is a no-op', () => {
  const b = R.emptyBucket('b');
  R.applyOwnGoal(b, {});
  R.applyOwnGoal(b, null);
  assert.equal(b.modifiers.ownGoal, 0);
});

test('resetMatch: clears boost + ownGoal modifier on the match block', () => {
  const b = R.emptyBucket('b');
  R.applyBoostConsumed(b, { delta: 50, player: { isMe: true } });
  R.applyOwnGoal(b, { deflector: { isMe: true } });
  R.resetMatch(b);
  assert.equal(b.match.boost, 0);
  assert.equal(b.match.modifiers.ownGoal, 0);
  assert.equal(b.totals.boost, 50, 'session totals untouched');
  assert.equal(b.modifiers.ownGoal, 1, 'session modifiers untouched');
});

// CROSSBAR hits and HARDEST hit follow the same me-only rule. The
// last-touch player on the event tells us who hit it.
test('applyCrossbarHit: only counts hits where I was the last touch', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 1000, ballSpeed: 100, ballLastTouch: { player: { isMe: false } } });
  assert.equal(b.crossbar.hits, 0);
  assert.equal(b.crossbar.hardest, null);
});

test('applyCrossbarHit: counts my hits and tracks my hardest', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 1500, ballSpeed: 80, ballLastTouch: { player: { isMe: true, name: 'me' } } });
  R.applyCrossbarHit(b, { impactForce: 2200, ballSpeed: 95, ballLastTouch: { player: { isMe: true, name: 'me' } } });
  assert.equal(b.crossbar.hits, 2);
  assert.equal(b.crossbar.hardest.impact, 2200);
});

test('applyCrossbarHit: missing ballLastTouch.player → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 5000 });
  assert.equal(b.crossbar.hits, 0);
});

// applyMatchStarted is the canonical "new match has begun" hook
// driven by the backend's _MatchStarted event. It clears the per-
// match block and stamps the guid so abandon detection has identity.
test('applyMatchStarted: clears match block and stamps guid', () => {
  const b = R.emptyBucket('b');
  b.match.result = 'loss';
  b.match.goals = 1;
  b.match.saves = 2;
  b.match.boost = 30;
  b.match.pickups = 80;
  b.match.modifiers.aerial = 1;
  b.match.modifiers.ownGoal = 1;
  R.applyMatchStarted(b, { matchGuid: 'g1' });
  assert.equal(b.match.result, null);
  assert.equal(b.match.goals, 0);
  assert.equal(b.match.boost, 0);
  assert.equal(b.match.pickups, 0);
  assert.equal(b.match.modifiers.aerial, 0);
  assert.equal(b.match.modifiers.ownGoal, 0);
  assert.equal(b.lastMatchGuid, 'g1');
});

test('applyMatchStarted: empty guid still resets and stamps empty', () => {
  const b = R.emptyBucket('b');
  b.match.goals = 3;
  R.applyMatchStarted(b, { matchGuid: '' });
  assert.equal(b.match.goals, 0);
  assert.equal(b.lastMatchGuid, '');
});

test('applyMatchStarted: bad payload is a no-op', () => {
  const b = R.emptyBucket('b');
  b.match.goals = 2;
  R.applyMatchStarted(b, null);
  assert.equal(b.match.goals, 2);
});

// applyMatchAbandoned tallies a forfeit loss when MatchDestroyed
// arrives without _MatchEnded having tallied first. Dedupes via
// lastTalliedGuid so a normal end-of-match destroy is a no-op.
test('applyMatchAbandoned: untallied match counts as a loss', () => {
  const b = R.emptyBucket('b');
  R.applyMatchStarted(b, { matchGuid: 'g1' });
  R.applyMatchAbandoned(b, 0);
  assert.equal(b.results.losses, 1);
  assert.equal(b.results.wins, 0);
  assert.deepEqual(b.results.last, ['loss']);
  assert.equal(b.match.result, 'loss');
  assert.equal(b.lastTalliedGuid, 'g1');
});

test('applyMatchAbandoned: already-tallied match is a no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMatchStarted(b, { matchGuid: 'g1' });
  R.applyMatchEnded(b, { matchGuid: 'g1', winnerTeamNum: 0 }, 0);
  assert.equal(b.results.wins, 1);
  R.applyMatchAbandoned(b, 0);
  assert.equal(b.results.wins, 1, 'win should survive');
  assert.equal(b.results.losses, 0, 'should not double-tally as loss');
});

test('applyMatchAbandoned: no current match → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMatchAbandoned(b, 0);
  assert.equal(b.results.losses, 0);
});

test('applyMatchAbandoned: myTeam null → no-op (can\'t determine result)', () => {
  const b = R.emptyBucket('b');
  R.applyMatchStarted(b, { matchGuid: 'g1' });
  R.applyMatchAbandoned(b, null);
  assert.equal(b.results.losses, 0);
});

test('applyMatchAbandoned: empty current guid → no-op (no identity to dedupe against)', () => {
  const b = R.emptyBucket('b');
  R.applyMatchStarted(b, { matchGuid: '' });
  R.applyMatchAbandoned(b, 0);
  assert.equal(b.results.losses, 0);
});

// Boost pickups: count of _BoostPickup events (one per pad grab), not
// a sum of boost percent. The delta still gates validity (we only
// count events that look like a real pickup with delta > 0) but each
// such event increments by 1.
test('emptyBucket: zeros pickups on totals and match', () => {
  const b = R.emptyBucket('b');
  assert.equal(b.totals.pickups, 0);
  assert.equal(b.match.pickups, 0);
});

test('applyBoostPickup: ignores opponents', () => {
  const b = R.emptyBucket('b');
  R.applyBoostPickup(b, { delta: 33, player: { isMe: false } });
  assert.equal(b.totals.pickups, 0);
  assert.equal(b.match.pickups, 0);
});

test('applyBoostPickup: counts each pickup once into both totals and match', () => {
  const b = R.emptyBucket('b');
  R.applyBoostPickup(b, { delta: 12, player: { isMe: true } });
  R.applyBoostPickup(b, { delta: 100, player: { isMe: true } });
  assert.equal(b.totals.pickups, 2);
  assert.equal(b.match.pickups, 2);
});

test('applyBoostPickup: missing or non-positive delta is a no-op', () => {
  const b = R.emptyBucket('b');
  R.applyBoostPickup(b, { player: { isMe: true } });
  R.applyBoostPickup(b, { delta: 0, player: { isMe: true } });
  R.applyBoostPickup(b, { delta: -5, player: { isMe: true } });
  R.applyBoostPickup(b, null);
  assert.equal(b.totals.pickups, 0);
  assert.equal(b.match.pickups, 0);
});

test('resetMatch: clears pickups on the match block, session preserved', () => {
  const b = R.emptyBucket('b');
  R.applyBoostPickup(b, { delta: 50, player: { isMe: true } });
  R.applyBoostPickup(b, { delta: 12, player: { isMe: true } });
  R.resetMatch(b);
  assert.equal(b.match.pickups, 0);
  assert.equal(b.totals.pickups, 2, 'session totals untouched');
});

// Goal modifiers: today only tracked at session scope. Mirror the
// goals/saves/demos pattern so the match block has its own modifier
// counts that reset between matches.
test('emptyBucket: includes match.modifiers with same keys as session modifiers', () => {
  const b = R.emptyBucket('b');
  assert.equal(typeof b.match.modifiers, 'object');
  assert.equal(b.match.modifiers.aerial, 0);
  assert.equal(b.match.modifiers.flipReset, 0);
  assert.equal(b.match.modifiers.hatTrick, 0);
  // Keys must match session modifiers exactly so the renderer can
  // iterate either with the same MOD_LABEL table.
  assert.deepEqual(
    Object.keys(b.match.modifiers).sort(),
    Object.keys(b.modifiers).sort(),
  );
});

test('applyGoalScored: bumps both session and match modifier counters', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, {
    scorer: { isMe: true },
    modifiers: { isAerialGoal: true, isFlipResetGoal: true },
  });
  assert.equal(b.modifiers.aerial, 1);
  assert.equal(b.match.modifiers.aerial, 1);
  assert.equal(b.modifiers.flipReset, 1);
  assert.equal(b.match.modifiers.flipReset, 1);
});

test('applyGoalScored: opponent goal does not touch either modifier block', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, {
    scorer: { isMe: false },
    modifiers: { isAerialGoal: true },
  });
  assert.equal(b.modifiers.aerial, 0);
  assert.equal(b.match.modifiers.aerial, 0);
});

// applyMyFastestGoal: from _GoalScored, tracks the fastest goal I've
// scored this session. Filters scorer.isMe. Skips own goals.
// Defensive ratchet so out-of-order events can't lower the record.
test('applyMyFastestGoal: ignores opponent goals', () => {
  const b = R.emptyBucket('b');
  R.applyMyFastestGoal(b, { scorer: { isMe: false }, goalSpeed: 150 });
  assert.equal(b.ball.fastestGoalKmh, null);
});

test('applyMyFastestGoal: ignores own goals even when scorer is me', () => {
  const b = R.emptyBucket('b');
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, isOwnGoal: true, goalSpeed: 150 });
  assert.equal(b.ball.fastestGoalKmh, null);
});

test('applyMyFastestGoal: records my goal speed', () => {
  const b = R.emptyBucket('b');
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, goalSpeed: 132.4 });
  assert.equal(b.ball.fastestGoalKmh, 132.4);
});

test('applyMyFastestGoal: keeps the fastest', () => {
  const b = R.emptyBucket('b');
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, goalSpeed: 100 });
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, goalSpeed: 180 });
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, goalSpeed: 120 });
  assert.equal(b.ball.fastestGoalKmh, 180);
});

test('applyMyFastestGoal: missing or non-finite goalSpeed → no-op', () => {
  const b = R.emptyBucket('b');
  R.applyMyFastestGoal(b, { scorer: { isMe: true } });
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, goalSpeed: 'fast' });
  R.applyMyFastestGoal(b, { scorer: { isMe: true }, goalSpeed: Infinity });
  R.applyMyFastestGoal(b, null);
  assert.equal(b.ball.fastestGoalKmh, null);
});

test('emptyBucket: fastestGoalKmh starts null', () => {
  const b = R.emptyBucket('b');
  assert.equal(b.ball.fastestGoalKmh, null);
});

test('resetMatch: zeros match.modifiers, session modifiers preserved', () => {
  const b = R.emptyBucket('b');
  R.applyGoalScored(b, {
    scorer: { isMe: true },
    modifiers: { isAerialGoal: true, isHatTrickGoal: true },
  });
  R.resetMatch(b);
  assert.equal(b.match.modifiers.aerial, 0);
  assert.equal(b.match.modifiers.hatTrick, 0);
  assert.equal(b.modifiers.aerial, 1, 'session modifiers untouched');
  assert.equal(b.modifiers.hatTrick, 1, 'session modifiers untouched');
});
