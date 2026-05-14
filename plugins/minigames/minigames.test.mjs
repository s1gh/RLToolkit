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
  assert.equal(s.activeTask, null);
  assert.equal(s.activeObjective, null);
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

test('wipeIfStaleBoot: stale boot clears activeObjective and activeTask', () => {
  const stale = R.emptySession('old-boot');
  stale.activeTask = { id: 'task-x' };
  stale.activeObjective = { id: 'obj-y', title: 'something' };
  const fresh = R.wipeIfStaleBoot(stale, 'new-boot');
  assert.equal(fresh.bootId, 'new-boot');
  assert.equal(fresh.activeTask, null);
  assert.equal(fresh.activeObjective, null);
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

test('endMatch: stamps endedAt, endLevel, result, and clears activeTask', () => {
  const s = R.emptySession('b');
  s.level = 3;
  R.startMatch(s, { matchGuid: 'g1', now: 1000 });
  s.activeTask = { id: 'x' };
  R.applyReward(s, 200); // pushes to level 4
  R.endMatch(s, { matchGuid: 'g1', now: 5000, result: 'win' });
  const m = s.matches[0];
  assert.equal(m.endedAt, 5000);
  assert.equal(m.endLevel, 4);
  assert.equal(m.result, 'win');
  assert.equal(s.activeTask, null);
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

test('emptySession has activeTask and activeObjective fields', () => {
  const s = R.emptySession('boot-1');
  assert.equal(s.bootId, 'boot-1');
  assert.ok('activeTask' in s);
  assert.ok('activeObjective' in s);
  assert.equal(s.activeTask, null);
  assert.equal(s.activeObjective, null);
  assert.equal(s.activeChallenge, undefined);
});

test('emptyRecords includes objectivesCompleted/objectivesFailed defaults', () => {
  const r = R.emptyRecords();
  assert.equal(r.objectivesCompleted, 0);
  assert.equal(r.objectivesFailed, 0);
});

test('migrateSession converts legacy activeChallenge to activeTask', () => {
  const legacy = {
    bootId: 'old', level: 3, xpInLevel: 50, xpToNext: 200,
    matches: [], activeChallenge: { id: 'demo-any', title: 't' },
    currentStreak: 0,
  };
  const migrated = R.migrateSession(legacy);
  assert.equal(migrated.activeTask.id, 'demo-any');
  assert.equal(migrated.activeObjective, null);
  assert.equal(migrated.activeChallenge, undefined);
});

test('migrateSession fills missing activeTask/activeObjective on modern shape', () => {
  const modern = { bootId: 'x', level: 1, xpInLevel: 0, xpToNext: 100, matches: [], currentStreak: 0 };
  const migrated = R.migrateSession(modern);
  assert.equal(migrated.activeTask, null);
  assert.equal(migrated.activeObjective, null);
});

test('instantiateObjective builds a runtime objective with no expiresAt', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 'Finish a round without conceding',
    tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  const now = 1700000000000;
  const rt = R.instantiateObjective(entry, now);
  assert.equal(rt.id, 'clean-sheet');
  assert.equal(rt.title, entry.title);
  assert.equal(rt.tier, 'hard');
  assert.equal(rt.xpReward, 250);
  assert.equal(rt.xpPenalty, 100);
  assert.equal(rt.drawnAt, now);
  assert.equal(rt.resolvedAt, null);
  assert.equal(rt.resolution, null);
  assert.equal(rt.progress, 0);
  assert.equal(rt.progressTarget, 1);
  assert.ok(!('expiresAt' in rt));
});

function makeObjectiveSession(xpInLevel, level) {
  const s = R.emptySession('b');
  s.level = level || 1;
  s.xpToNext = R.xpToNext(s.level);
  s.xpInLevel = xpInLevel || 0;
  return s;
}

test('resolveObjective(complete) adds xpReward and increments objectivesCompleted', () => {
  const s = makeObjectiveSession(0, 1);
  const recs = R.emptyRecords();
  s.activeObjective = R.instantiateObjective({
    id: 'clean-sheet', kind: 'objective', title: 't', tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  }, 1000);
  const result = R.resolveObjective(s, recs, 'complete', 2000);
  // 0 + 250: level 1 to 2 costs 100, level 2 to 3 costs 150. Lands at level 3, xpInLevel 0.
  assert.equal(s.level, 3);
  assert.equal(s.xpInLevel, 0);
  assert.equal(recs.objectivesCompleted, 1);
  assert.equal(recs.objectivesFailed, 0);
  assert.equal(s.activeObjective.resolvedAt, 2000);
  assert.equal(s.activeObjective.resolution, 'complete');
  assert.equal(result.xpDelta, 250);
});

test('resolveObjective(failed) subtracts xpPenalty and increments objectivesFailed', () => {
  const s = makeObjectiveSession(200, 1);
  s.xpToNext = 500; // synthetic high cap so xpInLevel stays in-level
  const recs = R.emptyRecords();
  s.activeObjective = R.instantiateObjective({
    id: 'clean-sheet', kind: 'objective', title: 't', tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  }, 1000);
  R.resolveObjective(s, recs, 'failed', 2000);
  assert.equal(s.xpInLevel, 100);
  assert.equal(s.level, 1);
  assert.equal(recs.objectivesCompleted, 0);
  assert.equal(recs.objectivesFailed, 1);
  assert.equal(s.activeObjective.resolution, 'failed');
});

test('resolveObjective(failed) with xpPenalty=0 does not change xp', () => {
  const s = makeObjectiveSession(50, 1);
  const recs = R.emptyRecords();
  s.activeObjective = R.instantiateObjective({
    id: 'mvp', kind: 'objective', title: 't', tier: 'hard',
    xp: { reward: 200, penalty: 0 },
    trigger: { kind: 'statfeed', filters: { eventName: 'MVP', targetIsMe: true } },
  }, 1000);
  R.resolveObjective(s, recs, 'failed', 2000);
  assert.equal(s.xpInLevel, 50);
  assert.equal(s.level, 1);
  assert.equal(recs.objectivesFailed, 1);
});

test('resolveObjective floors xp at level 1 / xpInLevel 0', () => {
  const s = makeObjectiveSession(10, 1);
  const recs = R.emptyRecords();
  s.activeObjective = R.instantiateObjective({
    id: 'clean-sheet', kind: 'objective', title: 't', tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  }, 1000);
  R.resolveObjective(s, recs, 'failed', 2000);
  assert.equal(s.level, 1);
  assert.equal(s.xpInLevel, 0);
  assert.equal(recs.objectivesFailed, 1);
});

test('resolveObjective returns no-op shape when session.activeObjective is null', () => {
  const s = R.emptySession('b');
  const recs = R.emptyRecords();
  const r = R.resolveObjective(s, recs, 'complete', 1000);
  // Null-guard contract: bails safely with a zeroed delta and no side effects.
  assert.equal(s.activeObjective, null);
  assert.deepEqual(r, { xpDelta: 0, deleveled: false });
  assert.equal(recs.objectivesCompleted, 0);
  assert.equal(recs.objectivesFailed, 0);
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

test('drawTask: returns a challenge whose id is in the pool', () => {
  const pool = [
    { id: 'e1', tier: 'easy' },
    { id: 'm1', tier: 'medium' },
    { id: 'h1', tier: 'hard' },
  ];
  let rngCalls = 0;
  const rng = () => { rngCalls += 1; return 0.0; }; // always picks the first eligible
  const got = C.drawTask({ pool, level: 1, bias: 'standard', rng, exclude: null });
  assert.ok(['e1', 'm1', 'h1'].includes(got.id));
  assert.ok(rngCalls >= 1);
});

test('drawTask: rerolls once when the first roll matches the exclude id', () => {
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
  const got = C.drawTask({ pool, level: 1, bias: 'standard', rng, exclude: 'e1' });
  assert.equal(got.id, 'e2');
  assert.equal(i, 4, 'expected exactly 4 rng draws (2 per drawOnce, called twice)');
});

test('drawTask: reroll that also collides still returns the collided result', () => {
  // Both drawOnce calls land on e1; the docblock says "accept whatever comes out".
  const pool = [
    { id: 'e1', tier: 'easy' },
    { id: 'e2', tier: 'easy' },
  ];
  let i = 0;
  const sequence = [0.0, 0.0, 0.0, 0.0];
  const rng = () => sequence[i++];
  const got = C.drawTask({ pool, level: 1, bias: 'standard', rng, exclude: 'e1' });
  assert.equal(got.id, 'e1');
  assert.equal(i, 4, 'expected exactly 4 rng draws even when reroll collides');
});

test('drawTask: empty pool returns null', () => {
  const got = C.drawTask({ pool: [], level: 1, bias: 'standard', rng: () => 0.5, exclude: null });
  assert.equal(got, null);
});

test('drawTask: single-entry pool returns it even when excluded (no infinite loop)', () => {
  const pool = [{ id: 'only', tier: 'easy' }];
  const got = C.drawTask({ pool, level: 1, bias: 'standard', rng: () => 0.0, exclude: 'only' });
  assert.equal(got.id, 'only');
});

// ---- predicate engine tests ----
// Build a tiny in-process bus so tests can fire events and watch the engine react.
function makeBus() {
  const subs = new Map();
  return {
    emit(name, payload) {
      const list = subs.get(name);
      if (!list) return;
      for (const fn of list.slice()) fn(payload);
    },
    on(name, fn) {
      if (!subs.has(name)) subs.set(name, []);
      subs.get(name).push(fn);
      return () => {
        const list = subs.get(name);
        if (!list) return;
        const i = list.indexOf(fn);
        if (i >= 0) list.splice(i, 1);
      };
    },
  };
}

function makeCtx(overrides) {
  const bus = makeBus();
  let completed = 0; let failed = 0;
  const ctx = {
    on: (name, fn) => bus.on(name, fn),
    matchState: () => 'live',
    myTeam: () => 0,
    opponents: () => [{ id: 'opp-1', name: 'Opponent1', team: 1 }, { id: 'opp-2', name: 'Opponent2', team: 1 }],
    stats: { SAVE: 'Save', SAVIOR: 'Savior', PLAYMAKER: 'Playmaker', MVP: 'MVP' },
    now: () => 0,
    onComplete: () => { completed += 1; },
    onFail: () => { failed += 1; },
    rng: () => 0,
    ...overrides,
  };
  return { ctx, bus, get completed() { return completed; }, get failed() { return failed; } };
}

test('validateEntry: rejects entries with missing required fields', () => {
  const errs1 = C.validateEntry({});
  assert.ok(errs1.length > 0);
  const errs2 = C.validateEntry({ id: 'x', title: 'X', tier: 'easy', trigger: { kind: 'goal' } });
  assert.deepEqual(errs2, []);
});

test('validateEntry: rejects unknown trigger kind', () => {
  const errs = C.validateEntry({ id: 'x', title: 'X', tier: 'easy', trigger: { kind: 'banana' } });
  assert.ok(errs.some((e) => /trigger\.kind/.test(e)));
});

test('validateEntry: rejects unknown tier', () => {
  const errs = C.validateEntry({ id: 'x', title: 'X', tier: 'epic', trigger: { kind: 'goal' } });
  assert.ok(errs.some((e) => /tier/.test(e)));
});

test('instantiate: fills defaults from tier when reward/penalty/time not set', () => {
  const { ctx } = makeCtx();
  const inst = C.instantiate({ id: 'x', title: 'X', tier: 'medium', trigger: { kind: 'save' } }, ctx);
  assert.equal(inst.reward, 90);
  assert.equal(inst.failPenalty, 45);
  assert.equal(inst.timeLimitMs, 75000);
});

test('instantiate: honors explicit reward/penalty/time overrides', () => {
  const { ctx } = makeCtx();
  const inst = C.instantiate({ id: 'x', title: 'X', tier: 'medium', timeLimitMs: 30000, reward: 200, failPenalty: 80, trigger: { kind: 'save' } }, ctx);
  assert.equal(inst.reward, 200);
  assert.equal(inst.failPenalty, 80);
  assert.equal(inst.timeLimitMs, 30000);
});

test('goal trigger: completes on _GoalScored when isMe and not own goal', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'g', title: 'Goal', tier: 'easy', trigger: { kind: 'goal', filters: { isMe: true, ownGoal: false } } }, w.ctx);
  const dispose = inst.arm();
  w.bus.emit('_GoalScored', { scorer: { isMe: false }, isOwnGoal: false, modifiers: {} });
  assert.equal(w.completed, 0);
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: {} });
  assert.equal(w.completed, 1);
  dispose();
});

test('goal trigger: modifiers filter requires the named modifier flag', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'g', title: 'Aerial', tier: 'hard', trigger: { kind: 'goal', filters: { isMe: true, modifiers: { aerial: true } } } }, w.ctx);
  inst.arm();
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: { aerial: false, bicycle: true } });
  assert.equal(w.completed, 0);
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: { aerial: true } });
  assert.equal(w.completed, 1);
});

test('goal trigger: goalSpeedMin filters out slow goals', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'g', title: 'Fast', tier: 'medium', trigger: { kind: 'goal', filters: { isMe: true, goalSpeedMin: 100 } } }, w.ctx);
  inst.arm();
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: {}, goalSpeed: 80 });
  assert.equal(w.completed, 0);
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: {}, goalSpeed: 120 });
  assert.equal(w.completed, 1);
});

test('count > 1: completes only after N triggers', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'tpl', title: 'Three saves', tier: 'medium', count: 3, trigger: { kind: 'save', filters: { isMe: true } } }, w.ctx);
  inst.arm();
  w.bus.emit('_StatfeedEvent', { eventName: 'Save', mainTarget: { isMe: true } });
  w.bus.emit('_StatfeedEvent', { eventName: 'Save', mainTarget: { isMe: true } });
  assert.equal(w.completed, 0);
  w.bus.emit('_StatfeedEvent', { eventName: 'Save', mainTarget: { isMe: true } });
  assert.equal(w.completed, 1);
});

test('demo trigger: requires attackerIsMe, excludes self-demo / team-demo by default', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'd', title: 'Demo', tier: 'medium', trigger: { kind: 'demo', filters: { attackerIsMe: true, excludeSelfDemo: true, excludeTeamDemo: true } } }, w.ctx);
  inst.arm();
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: false }, victim: { isMe: false }, isSelfDemo: false, isTeamDemo: false });
  assert.equal(w.completed, 0);
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: true }, victim: { isMe: false }, isSelfDemo: true, isTeamDemo: false });
  assert.equal(w.completed, 0);
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: true }, victim: { isMe: false }, isSelfDemo: false, isTeamDemo: true });
  assert.equal(w.completed, 0);
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: true }, victim: { isMe: false, id: 'opp-1' }, isSelfDemo: false, isTeamDemo: false });
  assert.equal(w.completed, 1);
});

test('demo trigger: victimTargetOpponent binds to one chosen opponent and substitutes {opponent}', () => {
  const w = makeCtx({ rng: () => 0 });
  const inst = C.instantiate({
    id: 'd', title: 'Demo {opponent} 2 times', tier: 'hard', count: 2,
    trigger: { kind: 'demo', filters: { attackerIsMe: true, victimTargetOpponent: true } },
  }, w.ctx);
  assert.equal(inst.title, 'Demo Opponent1 2 times');
  inst.arm();
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: true }, victim: { id: 'opp-2' }, isSelfDemo: false, isTeamDemo: false });
  assert.equal(w.completed, 0);
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: true }, victim: { id: 'opp-1' }, isSelfDemo: false, isTeamDemo: false });
  w.bus.emit('_PlayerDemolished', { attacker: { isMe: true }, victim: { id: 'opp-1' }, isSelfDemo: false, isTeamDemo: false });
  assert.equal(w.completed, 1);
});

test('targetOpponent: instantiate returns null when no opponents available', () => {
  const w = makeCtx({ opponents: () => [] });
  const inst = C.instantiate({ id: 'd', title: 'Demo {opponent}', tier: 'hard', trigger: { kind: 'demo', filters: { attackerIsMe: true, victimTargetOpponent: true } } }, w.ctx);
  assert.equal(inst, null);
});

test('targetOpponent: roster-loss of the bound opponent fails the challenge', () => {
  const w = makeCtx({ rng: () => 0 });
  const inst = C.instantiate({
    id: 'd', title: 'Demo {opponent}', tier: 'hard',
    trigger: { kind: 'demo', filters: { attackerIsMe: true, victimTargetOpponent: true } },
  }, w.ctx);
  inst.arm();
  // Opponent1 leaves the lobby.
  w.bus.emit('_RosterChanged', { players: [{ id: 'opp-2', name: 'Opponent2', team: 1 }] });
  assert.equal(w.failed, 1);
});

test('boostPickup trigger: minDelta filters out small-pad pickups', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'b', title: 'Big pads', tier: 'easy', count: 2, trigger: { kind: 'boostPickup', filters: { isMe: true, minDelta: 75 } } }, w.ctx);
  inst.arm();
  w.bus.emit('_BoostPickup', { player: { isMe: true }, delta: 12 });
  w.bus.emit('_BoostPickup', { player: { isMe: true }, delta: 12 });
  assert.equal(w.completed, 0);
  w.bus.emit('_BoostPickup', { player: { isMe: true }, delta: 88 });
  w.bus.emit('_BoostPickup', { player: { isMe: true }, delta: 100 });
  assert.equal(w.completed, 1);
});

test('boostPickup trigger: isBigPad:true filter passes only when payload flagged big', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'b', title: 'Big pads', tier: 'easy', count: 2, trigger: { kind: 'boostPickup', filters: { isMe: true, isBigPad: true } } }, w.ctx);
  inst.arm();
  w.bus.emit('_BoostPickup', { player: { isMe: true }, isBigPad: false, delta: 12 });
  w.bus.emit('_BoostPickup', { player: { isMe: true }, isBigPad: false, delta: 12 });
  assert.equal(w.completed, 0);
  w.bus.emit('_BoostPickup', { player: { isMe: true }, isBigPad: true, delta: 88 });
  w.bus.emit('_BoostPickup', { player: { isMe: true }, isBigPad: true, delta: 60 });
  assert.equal(w.completed, 1);
});

test('boostPickup trigger: isBigPad:false filter rejects big pads', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'b', title: 'Small pads', tier: 'easy', count: 1, trigger: { kind: 'boostPickup', filters: { isMe: true, isBigPad: false } } }, w.ctx);
  inst.arm();
  w.bus.emit('_BoostPickup', { player: { isMe: true }, isBigPad: true, delta: 100 });
  assert.equal(w.completed, 0);
  w.bus.emit('_BoostPickup', { player: { isMe: true }, isBigPad: false, delta: 12 });
  assert.equal(w.completed, 1);
});

test('boostConsumed trigger: sums delta until sumDelta is reached', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'c', title: 'Use 250 boost', tier: 'medium', trigger: { kind: 'boostConsumed', filters: { isMe: true, sumDelta: 250 } } }, w.ctx);
  inst.arm();
  w.bus.emit('_BoostConsumed', { player: { isMe: true }, delta: 100 });
  w.bus.emit('_BoostConsumed', { player: { isMe: true }, delta: 100 });
  assert.equal(w.completed, 0);
  w.bus.emit('_BoostConsumed', { player: { isMe: true }, delta: 60 });
  assert.equal(w.completed, 1);
});

test('touch trigger: consecutive resets on any other player', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 't', title: '5 in a row', tier: 'easy', count: 5, trigger: { kind: 'touch', filters: { isMe: true, consecutive: true } } }, w.ctx);
  inst.arm();
  // _BallHit ships the primary toucher under players[0], not a singular player.
  for (let i = 0; i < 4; i += 1) w.bus.emit('_BallHit', { players: [{ isMe: true }] });
  w.bus.emit('_BallHit', { players: [{ isMe: false }] });
  w.bus.emit('_BallHit', { players: [{ isMe: true }] });
  assert.equal(w.completed, 0);
  for (let i = 0; i < 4; i += 1) w.bus.emit('_BallHit', { players: [{ isMe: true }] });
  assert.equal(w.completed, 1);
});

test('statfeed trigger: matches eventName via ctx.stats and requires targetIsMe', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 's', title: 'Savior', tier: 'hard', trigger: { kind: 'statfeed', filters: { eventName: 'SAVIOR', targetIsMe: true } } }, w.ctx);
  inst.arm();
  w.bus.emit('_StatfeedEvent', { eventName: 'Savior', mainTarget: { isMe: false } });
  assert.equal(w.completed, 0);
  w.bus.emit('_StatfeedEvent', { eventName: 'Savior', mainTarget: { isMe: true } });
  assert.equal(w.completed, 1);
});

test('instantiate: timeLimitMs:null produces an open-ended runtime challenge', () => {
  const { ctx } = makeCtx();
  const inst = C.instantiate({
    id: 'open', title: 'Open-ended', tier: 'easy', timeLimitMs: null,
    trigger: { kind: 'matchEndCondition', filters: { noOwnGoal: true } },
  }, ctx);
  assert.equal(inst.timeLimitMs, null);
});

test('validateEntry: timeLimitMs:null is accepted', () => {
  const errs = C.validateEntry({ id: 'x', title: 'X', tier: 'easy', timeLimitMs: null, trigger: { kind: 'goal' } });
  assert.deepEqual(errs, []);
});

test('validateEntry: timeLimitMs:500 is rejected (below 1000ms floor)', () => {
  const errs = C.validateEntry({ id: 'x', title: 'X', tier: 'easy', timeLimitMs: 500, trigger: { kind: 'goal' } });
  assert.ok(errs.some((e) => /timeLimitMs/.test(e)));
});

test('arm: idempotent, second triggering event after completion is a no-op', () => {
  const w = makeCtx();
  const inst = C.instantiate({ id: 'g', title: 'Goal', tier: 'easy', trigger: { kind: 'goal', filters: { isMe: true } } }, w.ctx);
  inst.arm();
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: {} });
  assert.equal(w.completed, 1);
  w.bus.emit('_GoalScored', { scorer: { isMe: true }, isOwnGoal: false, modifiers: {} });
  assert.equal(w.completed, 1, 'onComplete must only fire once');
});

test('arm: idempotent, second roster-loss after fail is a no-op', () => {
  const w = makeCtx({ rng: () => 0 });
  const inst = C.instantiate({
    id: 'd', title: 'Demo {opponent}', tier: 'hard',
    trigger: { kind: 'demo', filters: { attackerIsMe: true, victimTargetOpponent: true } },
  }, w.ctx);
  inst.arm();
  w.bus.emit('_RosterChanged', { players: [{ id: 'opp-2' }] });
  assert.equal(w.failed, 1);
  w.bus.emit('_RosterChanged', { players: [] });
  assert.equal(w.failed, 1, 'onFail must only fire once');
});

test('challenges.json: every entry passes validateEntry', () => {
  const raw = readFileSync(new URL('./challenges.json', import.meta.url), 'utf8');
  const data = JSON.parse(raw);
  assert.ok(Array.isArray(data.challenges));
  assert.ok(data.challenges.length >= 15, 'at least 15 seed challenges expected, got ' + data.challenges.length);
  for (const entry of data.challenges) {
    const errs = C.validateEntry(entry);
    assert.deepEqual(errs, [], 'entry ' + entry.id + ' failed validation: ' + errs.join('; '));
  }
});

test('challenges.json: ids are unique', () => {
  const data = JSON.parse(readFileSync(new URL('./challenges.json', import.meta.url), 'utf8'));
  const ids = data.challenges.map((c) => c.id);
  assert.equal(new Set(ids).size, ids.length, 'duplicate ids in challenges.json');
});

test('validateEntry: accepts kind:"objective" with xp.reward and xp.penalty', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 'Finish a round without conceding',
    tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  assert.deepEqual(C.validateEntry(entry), []);
});

test('validateEntry: rejects objective entry without xp', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 't',
    tier: 'hard',
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /xp/.test(e)), `expected an /xp/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: rejects objective entry with timeLimitMs', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 't',
    tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    timeLimitMs: 30000,
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /timeLimitMs/.test(e)), `expected a /timeLimitMs/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: rejects task entry that declares xp', () => {
  const entry = {
    id: 'demo-any',
    kind: 'task',
    title: 't',
    tier: 'medium',
    xp: { reward: 90, penalty: 45 },
    trigger: { kind: 'demo', filters: { attackerIsMe: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /xp/.test(e)), `expected an /xp/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: treats missing kind as task (accepts entry without xp)', () => {
  const entry = {
    id: 'demo-any',
    title: 't',
    tier: 'medium',
    trigger: { kind: 'demo', filters: { attackerIsMe: true } },
  };
  assert.deepEqual(C.validateEntry(entry), []);
});

test('validateEntry: rejects objective entry with timeLimitMs:null', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 't',
    tier: 'hard',
    xp: { reward: 250, penalty: 100 },
    timeLimitMs: null,
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /timeLimitMs/.test(e)), `expected a /timeLimitMs/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: rejects task entry with xp:null', () => {
  const entry = {
    id: 'demo-any',
    kind: 'task',
    title: 't',
    tier: 'medium',
    xp: null,
    trigger: { kind: 'demo', filters: { attackerIsMe: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /xp/.test(e)), `expected an /xp/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: rejects objective xp.reward that is negative', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 't',
    tier: 'hard',
    xp: { reward: -1, penalty: 0 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /xp/.test(e)), `expected an /xp/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: rejects objective xp.reward that is NaN', () => {
  const entry = {
    id: 'clean-sheet',
    kind: 'objective',
    title: 't',
    tier: 'hard',
    xp: { reward: NaN, penalty: 0 },
    trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /xp/.test(e)), `expected an /xp/ error, got: ${JSON.stringify(errs)}`);
});

test('validateEntry: rejects unknown kind with a /kind/ error', () => {
  const entry = {
    id: 'demo-any',
    kind: 'lolwhat',
    title: 't',
    tier: 'medium',
    trigger: { kind: 'demo', filters: { attackerIsMe: true } },
  };
  const errs = C.validateEntry(entry);
  assert.ok(errs.some((e) => /kind/.test(e)), `expected a /kind/ error, got: ${JSON.stringify(errs)}`);
});

test('drawTask never returns an objective entry', () => {
  const pool = [
    { id: 'a', tier: 'easy', trigger: { kind: 'goal', filters: { isMe: true } } },
    { id: 'b', kind: 'objective', tier: 'hard', xp: { reward: 1, penalty: 0 },
      trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } } },
  ];
  // rng() returning 0.999 would pick the last entry in a uniform pool; with the
  // objective filtered out, the only candidate is 'a'.
  const rng = () => 0.999;
  const picked = C.drawTask({ pool, level: 1, bias: 'standard', rng, exclude: null });
  assert.equal(picked.id, 'a');
});

test('drawObjective only returns objective entries', () => {
  const pool = [
    { id: 'a', tier: 'easy', trigger: { kind: 'goal', filters: { isMe: true } } },
    { id: 'b', kind: 'objective', tier: 'hard', xp: { reward: 1, penalty: 0 },
      trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } } },
  ];
  const rng = () => 0.0;
  const picked = C.drawObjective({ pool, rng, exclude: new Set() });
  assert.equal(picked.id, 'b');
});

test('drawObjective uses uniform random (no difficulty bias)', () => {
  const pool = [
    { id: 'o1', kind: 'objective', tier: 'easy', xp: { reward: 1, penalty: 0 },
      trigger: { kind: 'matchEndCondition', filters: { noOwnGoal: true } } },
    { id: 'o2', kind: 'objective', tier: 'hard', xp: { reward: 1, penalty: 0 },
      trigger: { kind: 'matchEndCondition', filters: { cleanRound: true } } },
  ];
  let count1 = 0; let count2 = 0;
  for (let i = 0; i < 1000; i += 1) {
    const rng = () => i / 1000;
    const picked = C.drawObjective({ pool, rng, exclude: new Set() });
    if (picked.id === 'o1') count1 += 1; else count2 += 1;
  }
  // rng returns 0..0.999 linearly, so Math.floor(rng * 2) is 0 for i<500 and
  // 1 for i>=500 -> exact 500/500 split when uniform. The 50-count slop leaves
  // room if the implementation chooses to weight, in which case this fails.
  assert.ok(Math.abs(count1 - count2) < 50, `expected ~50/50, got ${count1}/${count2}`);
});

test('challenges.json: splits cleanly into task and objective pools', () => {
  const data = JSON.parse(readFileSync(new URL('./challenges.json', import.meta.url), 'utf8'));
  const all = data.challenges;
  const objectives = all.filter((c) => c.kind === 'objective');
  const tasks = all.filter((c) => (c.kind ?? 'task') === 'task');
  assert.equal(objectives.length, 4);
  assert.equal(tasks.length, all.length - 4);
  const objIds = objectives.map((c) => c.id).sort();
  assert.deepEqual(objIds, ['clean-sheet', 'goal-overtime', 'mvp', 'no-og']);
});
