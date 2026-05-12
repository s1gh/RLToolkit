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
