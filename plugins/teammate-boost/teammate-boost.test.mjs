import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const src = readFileSync(new URL('./app.js', import.meta.url), 'utf8');
const sandbox = { window: {} };
new Function('window', src)(sandbox.window);
const TB = sandbox.window.TeammateBoost;

test('TeammateBoost module exposes pure helpers on window', () => {
  assert.equal(typeof TB, 'object');
  assert.equal(typeof TB.collectTeammates, 'function');
  assert.equal(typeof TB.clamp, 'function');
  assert.equal(typeof TB.coerceConfig, 'function');
  assert.equal(typeof TB.isLowBoost, 'function');
});

test('clamp: value inside range is returned as-is', () => {
  assert.equal(TB.clamp(50, 0, 100), 50);
});

test('clamp: below min clamps to min', () => {
  assert.equal(TB.clamp(-5, 0, 100), 0);
});

test('clamp: above max clamps to max', () => {
  assert.equal(TB.clamp(150, 0, 100), 100);
});

test('clamp: non-numeric input returns min', () => {
  assert.equal(TB.clamp(NaN, 0, 100), 0);
  assert.equal(TB.clamp(null, 0, 100), 0);
  assert.equal(TB.clamp(undefined, 0, 100), 0);
});
