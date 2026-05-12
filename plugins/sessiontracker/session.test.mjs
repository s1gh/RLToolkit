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
  assert.deepEqual(b.totals, { goals: 0, saves: 0, demos: 0 });
  assert.equal(b.modifiers.aerial, 0);
  assert.equal(b.modifiers.poolShot, 0);
  assert.deepEqual(b.ball, { fastestKmh: null, myFastestHitKmh: null });
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
  assert.deepEqual(b.match, { result: null, goals: 0, saves: 0, demos: 0 });
});

test('resetMatch: zeros per-match counters and clears result', () => {
  const b = R.emptyBucket('b');
  b.match = { result: 'win', goals: 3, saves: 2, demos: 1 };
  R.resetMatch(b);
  assert.deepEqual(b.match, { result: null, goals: 0, saves: 0, demos: 0 });
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
