import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// Load state.js as a CommonJS-ish global blob, then expose its API.
const src = readFileSync(new URL('./state.js', import.meta.url), 'utf8');
const sandbox = { window: {} };
new Function('window', src)(sandbox.window);
const State = sandbox.window.SessionTrackerState;

test('computeStreaks: empty → null current & best', () => {
  const out = State.computeStreaks([]);
  assert.equal(out.current, null);
  assert.equal(out.best, null);
});

test('computeStreaks: single win → W1 / W1', () => {
  const out = State.computeStreaks([{ result: 'win' }]);
  assert.deepEqual(out.current, { kind: 'win', count: 1 });
  assert.deepEqual(out.best, { kind: 'win', count: 1 });
});

test('computeStreaks: W W L W W W → current W3, best W3', () => {
  const m = ['win', 'win', 'loss', 'win', 'win', 'win'].map((r) => ({ result: r }));
  const out = State.computeStreaks(m);
  assert.deepEqual(out.current, { kind: 'win', count: 3 });
  assert.deepEqual(out.best, { kind: 'win', count: 3 });
});

test('computeStreaks: W W W L L → current L2, best W3', () => {
  const m = ['win', 'win', 'win', 'loss', 'loss'].map((r) => ({ result: r }));
  const out = State.computeStreaks(m);
  assert.deepEqual(out.current, { kind: 'loss', count: 2 });
  assert.deepEqual(out.best, { kind: 'win', count: 3 });
});

test('recomputeTotals: empty → all zeros', () => {
  const t = State.recomputeTotals([]);
  assert.equal(t.wins, 0);
  assert.equal(t.losses, 0);
  assert.equal(t.goals, 0);
  assert.equal(t.timeInMatchesSec, 0);
  assert.equal(t.currentStreak, null);
  assert.equal(t.bestStreak, null);
});

test('recomputeTotals: 2W 1L with stats sums correctly', () => {
  const matches = [
    {
      result: 'win', durationSec: 300, mvp: true,
      myStats: { goals: 2, assists: 1, saves: 0, shots: 3, demos: 1, score: 540 },
      highlights: ['firstBlood'],
    },
    {
      result: 'loss', durationSec: 280, mvp: false,
      myStats: { goals: 1, assists: 0, saves: 2, shots: 2, demos: 0, score: 320 },
      highlights: [],
    },
    {
      result: 'win', durationSec: 320, mvp: false,
      myStats: { goals: 3, assists: 2, saves: 1, shots: 5, demos: 2, score: 720 },
      highlights: ['hatTrick', 'aerialGoal'],
    },
  ];
  const t = State.recomputeTotals(matches);
  assert.equal(t.wins, 2);
  assert.equal(t.losses, 1);
  assert.equal(t.goals, 6);
  assert.equal(t.assists, 3);
  assert.equal(t.saves, 3);
  assert.equal(t.shots, 10);
  assert.equal(t.demos, 3);
  assert.equal(t.mvps, 1);
  assert.equal(t.timeInMatchesSec, 900);
  assert.equal(t.hatTricks, 1);
  assert.equal(t.aerialGoals, 1);
  assert.deepEqual(t.currentStreak, { kind: 'win', count: 1 });
  assert.deepEqual(t.bestStreak, { kind: 'win', count: 1 });
});

test('buildMatchRecord: win for blue team, with my stats', () => {
  const matchEnded = {
    matchGuid: 'g1',
    winnerTeamNum: 0,
    scoreBlue: 4,
    scoreOrange: 2,
  };
  const matchView = { arena: 'Mannfield', raw: {} };
  const myStats   = { goals: 2, assists: 1, saves: 0, shots: 3, demos: 1, score: 540 };
  const accum = { durationSec: 300, highlights: ['firstBlood'], endedAt: '2026-05-05T18:12:34Z' };

  const rec = State.buildMatchRecord({
    matchEnded, matchView, myTeam: 0, myStats, mvp: true, accum,
  });
  assert.equal(rec.result, 'win');
  assert.equal(rec.scoreFor, 4);
  assert.equal(rec.scoreAgainst, 2);
  assert.equal(rec.durationSec, 300);
  assert.equal(rec.arena, 'Mannfield');
  assert.equal(rec.mvp, true);
  assert.deepEqual(rec.myStats, { goals: 2, assists: 1, saves: 0, shots: 3, demos: 1, score: 540 });
  assert.deepEqual(rec.highlights, ['firstBlood']);
  assert.equal(rec.endedAt, '2026-05-05T18:12:34Z');
});

test('buildMatchRecord: loss when winner is the other team', () => {
  const matchEnded = { winnerTeamNum: 1, scoreBlue: 1, scoreOrange: 3 };
  const myStats    = { goals: 0, assists: 0, saves: 1, shots: 1, demos: 0, score: 100 };
  const rec = State.buildMatchRecord({
    matchEnded, matchView: { arena: 'DFH', raw: {} }, myTeam: 0,
    myStats, mvp: false,
    accum: { durationSec: 280, highlights: [], endedAt: 't' },
  });
  assert.equal(rec.result, 'loss');
  assert.equal(rec.scoreFor, 1);
  assert.equal(rec.scoreAgainst, 3);
  assert.equal(rec.mvp, false);
});

test('buildMatchRecord: returns null if myTeam is null', () => {
  const matchEnded = { winnerTeamNum: 0, scoreBlue: 1, scoreOrange: 0 };
  const rec = State.buildMatchRecord({
    matchEnded, matchView: { arena: '', raw: {} }, myTeam: null,
    myStats: {}, mvp: false,
    accum: { durationSec: 0, highlights: [], endedAt: '' },
  });
  assert.equal(rec, null);
});
