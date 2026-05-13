import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const sandbox = { window: {} };
new Function('window', readFileSync(new URL('./background.js', import.meta.url), 'utf8'))(sandbox.window);
const R = sandbox.window.MinigamesReducers;

test('xpToNext: level 1 to 2 = 100', () => {
  assert.equal(R.xpToNext(1), 100);
});

test('xpToNext: level 2 to 3 = 150', () => {
  assert.equal(R.xpToNext(2), 150);
});

test('xpToNext: level 5 to 6 = 300', () => {
  assert.equal(R.xpToNext(5), 300);
});

test('xpToNext: level 10 to 11 = 550', () => {
  assert.equal(R.xpToNext(10), 550);
});

test('emptySession: starts at level 1, 0 XP, empty matches', () => {
  const s = R.emptySession('boot-x');
  assert.equal(s.bootId, 'boot-x');
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 0);
  assert.equal(s.xpToNext, 100);
  assert.deepEqual(s.matches, []);
  assert.equal(s.activeChallenge, null);
  assert.equal(s.currentStreak, 0);
});

test('applyReward: XP within the level', () => {
  const s = R.emptySession('b');
  R.applyReward(s, 40);
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 40);
  assert.equal(s.currentStreak, 1);
});

test('applyReward: crossing a level boundary advances level', () => {
  const s = R.emptySession('b');
  R.applyReward(s, 90);
  R.applyReward(s, 90);
  assert.equal(s.level, 2);
  assert.equal(s.xpInLevel, 80);
  assert.equal(s.xpToNext, 150);
  assert.equal(s.currentStreak, 2);
});

test('applyReward: streak resets on penalty then resumes on reward', () => {
  const s = R.emptySession('b');
  R.applyReward(s, 40);
  R.applyPenalty(s, 20);
  assert.equal(s.currentStreak, 0);
  R.applyReward(s, 40);
  assert.equal(s.currentStreak, 1);
});

test('applyPenalty: XP within the level (no delevel)', () => {
  const s = R.emptySession('b');
  R.applyReward(s, 80);
  const result = R.applyPenalty(s, 30);
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 50);
  assert.equal(result.deleveled, false);
});

test('applyPenalty: drops one level when xpInLevel would go negative', () => {
  const s = R.emptySession('b');
  R.applyReward(s, 100);                  // level 2, xpInLevel=0
  assert.equal(s.level, 2);
  const result = R.applyPenalty(s, 50);   // would go to -50 at level 2
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 50);          // xpToNext(1)=100, so 100 + (-50) = 50
  assert.equal(result.deleveled, true);
});

test('applyPenalty: cannot drop more than one level per call', () => {
  const s = R.emptySession('b');
  R.applyReward(s, 100);                  // level 2, xpInLevel=0
  const result = R.applyPenalty(s, 9999);
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 0);           // floored, not negative
  assert.equal(result.deleveled, true);
});

test('applyPenalty: floors at level 1, xp 0', () => {
  const s = R.emptySession('b');
  const result = R.applyPenalty(s, 50);
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 0);
  assert.equal(result.deleveled, false);
});

test('applyReward: clamps progress at level cap 30', () => {
  const s = R.emptySession('b');
  s.level = 30;
  s.xpInLevel = 0;
  s.xpToNext = R.xpToNext(30);
  R.applyReward(s, 99999);
  assert.equal(s.level, 30);
  assert.equal(s.xpInLevel, s.xpToNext);  // pinned at full bar
});

test('applyReward: crossing into the cap clamps excess XP', () => {
  const s = R.emptySession('b');
  s.level = 29;
  s.xpInLevel = 0;
  s.xpToNext = R.xpToNext(29);
  R.applyReward(s, 99999);
  assert.equal(s.level, 30);
  assert.equal(s.xpInLevel, R.xpToNext(30));  // pinned at full after the loop exits at cap
});

test('wipeIfStaleBoot: stale bootId returns fresh session', () => {
  const stale = R.emptySession('old-boot');
  stale.level = 5; stale.xpInLevel = 200;
  const fresh = R.wipeIfStaleBoot(stale, 'new-boot');
  assert.equal(fresh.bootId, 'new-boot');
  assert.equal(fresh.level, 1);
  assert.equal(fresh.xpInLevel, 0);
});

test('wipeIfStaleBoot: same bootId returns the same session', () => {
  const s = R.emptySession('boot');
  s.level = 5;
  const out = R.wipeIfStaleBoot(s, 'boot');
  assert.equal(out, s);
  assert.equal(out.level, 5);
});

test('wipeIfStaleBoot: null/empty input returns fresh', () => {
  const fresh = R.wipeIfStaleBoot(null, 'boot');
  assert.equal(fresh.bootId, 'boot');
  assert.equal(fresh.level, 1);
});

test('startMatch: pushes a fresh match entry with start level snapshot', () => {
  const s = R.emptySession('b');
  s.level = 5; s.xpInLevel = 120;
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  assert.equal(s.matches.length, 1);
  const m = s.matches[0];
  assert.equal(m.matchGuid, 'g1');
  assert.equal(m.startedAt, 1000);
  assert.equal(m.endedAt, null);
  assert.equal(m.startLevel, 5);
  assert.equal(m.endLevel, 5);
  assert.equal(m.completed, 0);
  assert.equal(m.failed, 0);
  assert.equal(m.timedOut, 0);
  assert.equal(m.xpGained, 0);
  assert.equal(m.result, null);
});

test('startMatch: same matchGuid is a no-op (idempotent)', () => {
  const s = R.emptySession('b');
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  R.startMatch(s, { matchGuid: 'g1', now: 2000 });
  assert.equal(s.matches.length, 1);
  assert.equal(s.matches[0].startedAt, 1000);
});

test('recordResolution: completed increments completed + xpGained', () => {
  const s = R.emptySession('b');
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  R.recordResolution(s, { outcome: 'completed', xpDelta: +90 });
  const m = s.matches[0];
  assert.equal(m.completed, 1);
  assert.equal(m.xpGained, 90);
});

test('recordResolution: failed increments failed + negative xpDelta', () => {
  const s = R.emptySession('b');
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  R.recordResolution(s, { outcome: 'failed', xpDelta: -45 });
  const m = s.matches[0];
  assert.equal(m.failed, 1);
  assert.equal(m.xpGained, -45);
});

test('recordResolution: timedOut increments timedOut (also counts as fail penalty)', () => {
  const s = R.emptySession('b');
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  R.recordResolution(s, { outcome: 'timedOut', xpDelta: -90 });
  const m = s.matches[0];
  assert.equal(m.timedOut, 1);
  assert.equal(m.xpGained, -90);
});

test('endMatch: stamps endedAt, endLevel, result, and clears activeChallenge', () => {
  const s = R.emptySession('b');
  s.level = 3;
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  s.activeChallenge = { id: 'x' };
  R.applyReward(s, 200); // pushes to level 4
  R.endMatch(s, { matchGuid: 'g1', now: 5000, result: 'win' });
  const m = s.matches[0];
  assert.equal(m.endedAt, 5000);
  assert.equal(m.endLevel, 4);
  assert.equal(m.result, 'win');
  assert.equal(s.activeChallenge, null);
});

test('endMatch: ignored if matchGuid does not match current entry', () => {
  const s = R.emptySession('b');
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  R.endMatch(s, { matchGuid: 'g-other', now: 5000, result: 'win' });
  assert.equal(s.matches[0].endedAt, null);
});

test('takeRecord: bumps highestLevel only when surpassed', () => {
  const recs = R.emptyRecords();
  recs.highestLevel = 5;
  R.takeRecord(recs, { level: 4 });
  assert.equal(recs.highestLevel, 5);
  R.takeRecord(recs, { level: 7 });
  assert.equal(recs.highestLevel, 7);
});

test('takeRecord: bumps longestStreak only when surpassed', () => {
  const recs = R.emptyRecords();
  recs.longestStreak = 3;
  R.takeRecord(recs, { streak: 2 });
  assert.equal(recs.longestStreak, 3);
  R.takeRecord(recs, { streak: 6 });
  assert.equal(recs.longestStreak, 6);
});

test('takeRecord: bumps bestMatchXp only when surpassed (positive only)', () => {
  const recs = R.emptyRecords();
  recs.bestMatchXp = 100;
  R.takeRecord(recs, { matchXp: 50 });
  assert.equal(recs.bestMatchXp, 100);
  R.takeRecord(recs, { matchXp: 250 });
  assert.equal(recs.bestMatchXp, 250);
  R.takeRecord(recs, { matchXp: -500 });
  assert.equal(recs.bestMatchXp, 250); // negatives never lower the best
});

const csb = { window: {} };
new Function('window', readFileSync(new URL('./challenges.js', import.meta.url), 'utf8'))(csb.window);
const C = csb.window.MinigamesChallenges;

test('tierDefaults: easy/medium/hard have reward, penalty, timeLimitMs', () => {
  assert.equal(C.tierDefaults.easy.reward, 40);
  assert.equal(C.tierDefaults.easy.failPenalty, 20);
  assert.equal(C.tierDefaults.easy.timeLimitMs, 45000);
  assert.equal(C.tierDefaults.medium.reward, 90);
  assert.equal(C.tierDefaults.medium.failPenalty, 45);
  assert.equal(C.tierDefaults.medium.timeLimitMs, 75000);
  assert.equal(C.tierDefaults.hard.reward, 180);
  assert.equal(C.tierDefaults.hard.failPenalty, 90);
  assert.equal(C.tierDefaults.hard.timeLimitMs, 120000);
});

test('interpolateWeights: standard@level=1 matches the anchor row', () => {
  const w = C.interpolateWeights(1, 'standard');
  assert.equal(w.easy.toFixed(2), '0.60');
  assert.equal(w.medium.toFixed(2), '0.30');
  assert.equal(w.hard.toFixed(2), '0.10');
});

test('interpolateWeights: standard@level=5 matches the anchor row', () => {
  const w = C.interpolateWeights(5, 'standard');
  assert.equal(w.easy.toFixed(2), '0.40');
  assert.equal(w.medium.toFixed(2), '0.40');
  assert.equal(w.hard.toFixed(2), '0.20');
});

test('interpolateWeights: linear interpolation between anchors (level 3, standard)', () => {
  // anchors: level 1 -> easy 0.60, level 5 -> easy 0.40. midway-ish (level 3 = 2/4 between) -> 0.50.
  const w = C.interpolateWeights(3, 'standard');
  assert.equal(w.easy.toFixed(2), '0.50');
});

test('interpolateWeights: standard@level=25 clamps to the level-20+ row', () => {
  const w = C.interpolateWeights(25, 'standard');
  assert.equal(w.easy.toFixed(2), '0.15');
  assert.equal(w.hard.toFixed(2), '0.40');
});

test('interpolateWeights: eased weights hard at 0.00 at level 1', () => {
  const w = C.interpolateWeights(1, 'eased');
  assert.equal(w.hard.toFixed(2), '0.00');
});

test('interpolateWeights: sharp weights easy at 0.10 at level 10+', () => {
  const w = C.interpolateWeights(10, 'sharp');
  assert.equal(w.easy.toFixed(2), '0.10');
});

test('draw: returns a challenge whose id is in the pool', () => {
  const pool = [
    { id: 'e1', tier: 'easy' },
    { id: 'm1', tier: 'medium' },
    { id: 'h1', tier: 'hard' },
  ];
  let rngCalls = 0;
  const rng = () => { rngCalls += 1; return 0.0; }; // always picks the first eligible
  const got = C.draw({ pool, level: 1, bias: 'standard', rng, exclude: null });
  assert.ok(['e1', 'm1', 'h1'].includes(got.id));
  assert.ok(rngCalls >= 1);
});

test('draw: rerolls once when the first roll matches the exclude id', () => {
  // pool only has the easy tier present.
  // drawOnce #1 consumes (0.0, 0.0): r1=0 -> easy tier, r2=floor(0*2)=0 -> e1.
  // result.id === 'e1' matches exclude, pool.length > 1, so reroll.
  // drawOnce #2 consumes (0.0, 0.99): r1=0 -> easy tier, r2=floor(0.99*2)=1 -> e2.
  const pool = [
    { id: 'e1', tier: 'easy' },
    { id: 'e2', tier: 'easy' },
  ];
  let i = 0;
  const sequence = [0.0, 0.0, 0.0, 0.99];
  const rng = () => sequence[i++];
  const got = C.draw({ pool, level: 1, bias: 'standard', rng, exclude: 'e1' });
  assert.equal(got.id, 'e2');
  assert.equal(i, 4, 'expected exactly 4 rng draws (2 per drawOnce, called twice)');
});

test('draw: reroll that also collides still returns the collided result', () => {
  // Both drawOnce calls land on e1; the docblock says "accept whatever comes out".
  const pool = [
    { id: 'e1', tier: 'easy' },
    { id: 'e2', tier: 'easy' },
  ];
  let i = 0;
  const sequence = [0.0, 0.0, 0.0, 0.0];
  const rng = () => sequence[i++];
  const got = C.draw({ pool, level: 1, bias: 'standard', rng, exclude: 'e1' });
  assert.equal(got.id, 'e1');
  assert.equal(i, 4, 'expected exactly 4 rng draws even when reroll collides');
});

test('draw: empty pool returns null', () => {
  const got = C.draw({ pool: [], level: 1, bias: 'standard', rng: () => 0.5, exclude: null });
  assert.equal(got, null);
});

test('draw: single-entry pool returns it even when excluded (no infinite loop)', () => {
  const pool = [{ id: 'only', tier: 'easy' }];
  const got = C.draw({ pool, level: 1, bias: 'standard', rng: () => 0.0, exclude: 'only' });
  assert.equal(got.id, 'only');
});
