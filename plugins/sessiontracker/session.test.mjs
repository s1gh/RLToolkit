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
  assert.deepEqual(b.ball, { fastestKmh: null });
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

test('applyFastestShot: first value seeds fastestKmh', () => {
  const b = R.emptyBucket('b');
  R.applyFastestShot(b, { speed: 110.5 });
  assert.equal(b.ball.fastestKmh, 110.5);
});

test('applyFastestShot: strictly greater replaces', () => {
  const b = R.emptyBucket('b');
  R.applyFastestShot(b, { speed: 110 });
  R.applyFastestShot(b, { speed: 120 });
  assert.equal(b.ball.fastestKmh, 120);
});

test('applyFastestShot: equal or lower preserves existing', () => {
  const b = R.emptyBucket('b');
  R.applyFastestShot(b, { speed: 130 });
  R.applyFastestShot(b, { speed: 130 });
  R.applyFastestShot(b, { speed: 100 });
  assert.equal(b.ball.fastestKmh, 130);
});

test('applyFastestShot: non-numeric speed ignored', () => {
  const b = R.emptyBucket('b');
  R.applyFastestShot(b, { speed: 'fast' });
  R.applyFastestShot(b, {});
  assert.equal(b.ball.fastestKmh, null);
});

test('applyCrossbarHit: first hit seeds hits and hardest', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, {
    impactForce: 800, ballSpeed: 130,
    ballLastTouch: { player: { name: 'Apparition', team: 1, isMe: false } },
  });
  assert.equal(b.crossbar.hits, 1);
  assert.equal(b.crossbar.hardest.impact, 800);
  assert.equal(b.crossbar.hardest.speed, 130);
  assert.equal(b.crossbar.hardest.player.name, 'Apparition');
  assert.equal(b.crossbar.hardest.player.team, 1);
  assert.equal(b.crossbar.hardest.player.isMe, false);
  assert.equal(typeof b.crossbar.hardest.at, 'string');
});

test('applyCrossbarHit: strictly greater impact replaces hardest', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 800, ballSpeed: 130, ballLastTouch: { player: { name: 'A', team: 0, isMe: true } } });
  R.applyCrossbarHit(b, { impactForce: 900, ballSpeed: 140, ballLastTouch: { player: { name: 'B', team: 1, isMe: false } } });
  assert.equal(b.crossbar.hits, 2);
  assert.equal(b.crossbar.hardest.impact, 900);
  assert.equal(b.crossbar.hardest.player.name, 'B');
});

test('applyCrossbarHit: equal impact keeps the earlier record', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 800, ballSpeed: 130, ballLastTouch: { player: { name: 'A', team: 0, isMe: true } } });
  R.applyCrossbarHit(b, { impactForce: 800, ballSpeed: 150, ballLastTouch: { player: { name: 'B', team: 1, isMe: false } } });
  assert.equal(b.crossbar.hits, 2);
  assert.equal(b.crossbar.hardest.player.name, 'A');
  assert.equal(b.crossbar.hardest.speed, 130);
});

test('applyCrossbarHit: missing impactForce → still counted, hardest unchanged', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { ballLastTouch: { player: { name: 'A', team: 0 } } });
  assert.equal(b.crossbar.hits, 1);
  assert.equal(b.crossbar.hardest, null);
});

test('applyCrossbarHit: missing player still records hardest with null player', () => {
  const b = R.emptyBucket('b');
  R.applyCrossbarHit(b, { impactForce: 750, ballSpeed: 120 });
  assert.equal(b.crossbar.hits, 1);
  assert.equal(b.crossbar.hardest.impact, 750);
  assert.equal(b.crossbar.hardest.player, null);
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
