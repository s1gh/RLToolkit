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
