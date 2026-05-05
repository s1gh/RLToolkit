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
