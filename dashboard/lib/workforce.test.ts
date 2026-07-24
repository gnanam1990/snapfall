import assert from 'node:assert/strict';
import test from 'node:test';

import { BUILD_MONITOR_MANIFEST, validHireInput } from './workforce';

test('build-monitor catalog projection keeps the bounded permissions visible', () => {
  assert.equal(BUILD_MONITOR_MANIFEST.id, 'build-monitor');
  assert.deepEqual(BUILD_MONITOR_MANIFEST.permissions, ['Read-only repo', 'No payments', 'No shell']);
  assert.equal(BUILD_MONITOR_MANIFEST.checklistPath, '.snapfall/milestone.json');
});

test('hire input requires a repository and positive two-decimal quote', () => {
  assert.equal(validHireInput('/work/acme', '25.00'), true);
  assert.equal(validHireInput('', '25.00'), false);
  assert.equal(validHireInput('/work/acme', '0'), false);
  assert.equal(validHireInput('/work/acme', '2.345'), false);
});
