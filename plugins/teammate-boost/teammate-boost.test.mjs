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
