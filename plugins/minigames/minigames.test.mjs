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
