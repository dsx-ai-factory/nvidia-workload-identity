const assert = require('node:assert/strict');
const { join } = require('node:path');
const { test } = require('node:test');
const { createFixture } = require('./helpers/fixture.cjs');
const { validateVersion } = require('../rc/validate-version.cjs');

const branch = 'release/1.15.0';
const version = '1.15.0-rc.1';

test('accepts positive RC sequence numbers for the configured release', () => {
  for (const value of [version, '1.15.0-rc.2', '1.15.0-rc.10', '1.15.0-rc.1234']) {
    assert.doesNotThrow(() => validateVersion(branch, value), value);
  }
});

const invalidBranches = [
  undefined, '', 'master', 'main', 'release/1.14.9', 'release/1.15.1',
  'release/1.16.0', 'refs/heads/release/1.15.0', 'release/1.15.0/extra',
  'Release/1.15.0', ' release/1.15.0', 'release/1.15.0 ', 'release/1.15.0\n',
];

test('rejects every branch except the exact configured RC branch', () => {
  for (const value of invalidBranches) {
    assert.throws(() => validateVersion(value, version), JSON.stringify(value));
  }
  for (const [candidateBranch, candidateVersion] of [
    ['release/1.14.9', '1.14.9-rc.1'],
    ['release/1.16.0', '1.16.0-rc.1'],
  ]) {
    assert.throws(() => validateVersion(candidateBranch, candidateVersion), candidateBranch);
  }
});

const invalidVersions = [
  undefined, '', '1.15.0', 'v1.15.0-rc.1', '1.14.9-rc.1', '1.15.1-rc.1',
  '1.16.0-rc.1', '2.0.0-rc.1', '01.15.0-rc.1', '1.015.0-rc.1', '1.15.00-rc.1',
  '1.15.0-beta.1', '1.15.0-RC.1', '1.15.0-rc', '1.15.0-rc.', '1.15.0-rc.0',
  '1.15.0-rc.01', '1.15.0-rc.00', '1.15.0-rc.-1', '1.15.0-rc.+1',
  '1.15.0-rc.1.2', '1.15.0-rc.1e2', '1.15.0-rc.1+build', '1.15.0-rc.1suffix',
  ' 1.15.0-rc.1', '1.15.0-rc.1 ', '1.15.0-rc.1\n', '1.15.0-rc.1\r\n',
];

test('rejects mismatched versions, nonpositive sequences, and noncanonical input', () => {
  for (const value of invalidVersions) {
    assert.throws(() => validateVersion(branch, value), JSON.stringify(value));
  }
});

test('CLI reads GITHUB_REF_NAME and RC_VERSION and fails closed', async (t) => {
  const fixture = await createFixture(t);
  const cli = join(__dirname, '../rc/validate-version.cjs');
  const cases = [
    { name: 'valid RC', branch, version, status: 0 },
    { name: 'multidigit RC', branch, version: '1.15.0-rc.10', status: 0 },
    { name: 'missing branch', version, status: 1 },
    { name: 'missing version', branch, status: 1 },
    { name: 'missing both inputs', status: 1 },
    { name: 'stable branch', branch: 'master', version, status: 1 },
    {
      name: 'unconfigured branch with matching version',
      branch: 'release/1.16.0', version: '1.16.0-rc.1', status: 1,
    },
    { name: 'wrong version base', branch, version: '1.16.0-rc.1', status: 1 },
    { name: 'stable version', branch, version: '1.15.0', status: 1 },
    { name: 'zero sequence', branch, version: '1.15.0-rc.0', status: 1 },
    { name: 'leading zero sequence', branch, version: '1.15.0-rc.01', status: 1 },
    { name: 'trailing newline', branch, version: '1.15.0-rc.1\n', status: 1 },
  ];
  for (const example of cases) {
    await t.test(example.name, async () => {
      const extraEnv = {};
      if (example.branch !== undefined) extraEnv.GITHUB_REF_NAME = example.branch;
      if (example.version !== undefined) extraEnv.RC_VERSION = example.version;
      const result = await fixture.run(process.execPath, [cli], {
        extraEnv,
        timeout: 5_000,
      });
      assert.equal(result.signal, null, result.stderr);
      assert.equal(result.status, example.status, result.stderr);
    });
  }
});
