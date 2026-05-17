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

test('coerceConfig: null returns defaults', () => {
  const c = TB.coerceConfig(null);
  assert.deepEqual(c, {
    gaugeStyle: 'radial',
    colorScheme: 'cyan',
    lowBoostThreshold: 20,
    showNames: true,
  });
});

test('coerceConfig: partial object fills missing keys with defaults', () => {
  const c = TB.coerceConfig({ colorScheme: 'violet' });
  assert.equal(c.gaugeStyle, 'radial');
  assert.equal(c.colorScheme, 'violet');
  assert.equal(c.lowBoostThreshold, 20);
  assert.equal(c.showNames, true);
});

test('coerceConfig: unsupported gaugeStyle falls back to radial', () => {
  assert.equal(TB.coerceConfig({ gaugeStyle: 'bar' }).gaugeStyle, 'radial');
  assert.equal(TB.coerceConfig({ gaugeStyle: 'column' }).gaugeStyle, 'radial');
  assert.equal(TB.coerceConfig({ gaugeStyle: 'nonsense' }).gaugeStyle, 'radial');
});

test('coerceConfig: unsupported colorScheme falls back to cyan', () => {
  assert.equal(TB.coerceConfig({ colorScheme: 'pink' }).colorScheme, 'cyan');
});

test('coerceConfig: lowBoostThreshold clamps to 0..100 and rounds', () => {
  assert.equal(TB.coerceConfig({ lowBoostThreshold: -10 }).lowBoostThreshold, 0);
  assert.equal(TB.coerceConfig({ lowBoostThreshold: 200 }).lowBoostThreshold, 100);
  assert.equal(TB.coerceConfig({ lowBoostThreshold: 17.6 }).lowBoostThreshold, 18);
  assert.equal(TB.coerceConfig({ lowBoostThreshold: 'oops' }).lowBoostThreshold, 20);
});

test('coerceConfig: showNames coerces to boolean', () => {
  assert.equal(TB.coerceConfig({ showNames: 0 }).showNames, false);
  assert.equal(TB.coerceConfig({ showNames: 1 }).showNames, true);
  assert.equal(TB.coerceConfig({ showNames: undefined }).showNames, true);
});

test('isLowBoost: strictly less than threshold is true', () => {
  assert.equal(TB.isLowBoost(19, 20), true);
});

test('isLowBoost: equal to threshold is false', () => {
  assert.equal(TB.isLowBoost(20, 20), false);
});

test('isLowBoost: above threshold is false', () => {
  assert.equal(TB.isLowBoost(75, 20), false);
});

test('isLowBoost: threshold 0 always returns false', () => {
  assert.equal(TB.isLowBoost(0, 0), false);
  assert.equal(TB.isLowBoost(50, 0), false);
});

test('isLowBoost: non-numeric boost returns false', () => {
  assert.equal(TB.isLowBoost(null, 20), false);
  assert.equal(TB.isLowBoost(undefined, 20), false);
});
