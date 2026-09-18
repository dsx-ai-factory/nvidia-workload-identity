const assert = require('node:assert/strict');
const { appendFile, copyFile, mkdir } = require('node:fs/promises');
const { join } = require('node:path');
const { test } = require('node:test');
const { pathToFileURL } = require('node:url');
const { createFixture } = require('./helpers/fixture.cjs');

test('semantic-release publishes only the expected RC tags to local bare Git', async (t) => {
  const fixture = await createFixture(t);
  const work = join(fixture.root, 'work');
  const remote = join(fixture.root, 'origin.git');
  const repositoryUrl = pathToFileURL(remote).href;
  await mkdir(work);
  await mkdir(join(work, '.github/rc'), { recursive: true });
  await copyFile(join(__dirname, '../release-rc.config.cjs'), join(work, '.releaserc.cjs'));
  await copyFile(join(__dirname, '../release-rc.config.cjs'), join(work, '.github/release-rc.config.cjs'));
  await copyFile(join(__dirname, '../rc/validate-version.cjs'), join(work, '.github/rc/validate-version.cjs'));
  await copyFile(join(__dirname, '../rc/verify-existing-tag.sh'), join(work, '.github/rc/verify-existing-tag.sh'));
  await fixture.git(fixture.root, 'init', '--bare', '--initial-branch=master', '--template=', remote);
  await fixture.git(work, 'init', '--initial-branch=master', '--template=');

  // Generated repositories have no inherited hooks, helpers, signing, or submodules.
  const gitConfig = `
[user]
  name = RC Test
  email = rc-test@example.invalid
[core]
  hooksPath = /dev/null
[credential]
  helper =
[commit]
  gpgSign = false
[tag]
  gpgSign = false
[protocol]
  allow = never
[protocol "file"]
  allow = always
[submodule]
  recurse = false
[fetch]
  recurseSubmodules = false
[push]
  recurseSubmodules = no
[gc]
  auto = 0
[maintenance]
  auto = false
`;
  await appendFile(join(work, '.git/config'), gitConfig);
  await appendFile(join(remote, 'config'), gitConfig);
  const git = (...args) => fixture.git(work, ...args);
  const remoteGit = (...args) => fixture.git(remote, ...args);
  await git('remote', 'add', 'origin', repositoryUrl);

  async function commit(message) {
    await git('commit', '--allow-empty', '-m', message);
    return git('rev-parse', 'HEAD');
  }

  function runRelease(dryRun = false) {
    return fixture.run(process.execPath, [
      join(__dirname, 'helpers/release.mjs'), repositoryUrl, String(dryRun),
    ], { cwd: work, timeout: 30_000 });
  }

  async function release(dryRun = false) {
    const result = await runRelease(dryRun);
    assert.equal(result.status, 0, result.stderr);
    return JSON.parse(result.stdout);
  }

  function verifyExistingTag(version, sha) {
    return fixture.run('/bin/bash', ['.github/rc/verify-existing-tag.sh'], {
      cwd: work,
      extraEnv: {
        RC_TAG: `v${version}`,
        RC_VERSION: version,
        GITHUB_SHA: sha,
        GITHUB_REF_NAME: 'release/1.15.0',
      },
      timeout: 10_000,
    });
  }

  async function assertTags(expected) {
    for (const execute of [git, remoteGit]) {
      const refs = await execute('for-each-ref', '--format=%(refname:short) %(objectname)', 'refs/tags');
      const observed = Object.fromEntries(refs.split('\n').filter(Boolean).map((ref) => ref.split(' ')));
      assert.deepEqual(observed, expected);
    }
  }

  const stable = await commit('fix: establish stable identity release');
  await git('tag', 'v1.14.9');
  await git('tag', 'v1');
  await git('push', '--set-upstream', 'origin', 'master', '--tags');
  const stableTags = { v1: stable, 'v1.14.9': stable };
  let master;

  await t.test('docs on master do not release or move the stable tags', async () => {
    master = await commit('docs: clarify stable identity documentation');
    await git('push', 'origin', 'master');
    assert.equal(await release(), false);
    await assertTags(stableTags);
  });

  await git('switch', '--create', 'release/1.15.0');
  const feature = await commit('feat(DSX-1575): add identity release candidate');
  await git('push', '--set-upstream', 'origin', 'release/1.15.0');

  await t.test('feature dry-run computes rc.1 without creating any tag', async () => {
    const result = await release(true);
    assert.equal(result.lastRelease.version, '1.14.9');
    assert.equal(result.lastRelease.gitHead, stable);
    assert.equal(result.nextRelease.version, '1.15.0-rc.1');
    assert.equal(result.nextRelease.gitTag, 'v1.15.0-rc.1');
    assert.equal(result.nextRelease.channel, 'rc');
    assert.equal(result.nextRelease.type, 'minor');
    assert.equal(result.nextRelease.gitHead, feature);
    assert.match(result.nextRelease.notes, /add identity release candidate/);
    assert.doesNotMatch(result.nextRelease.notes, /clarify stable identity documentation/);
    await assertTags(stableTags);
  });

  const rc1Tags = { ...stableTags, 'v1.15.0-rc.1': feature };
  await t.test('actual publication pushes rc.1 to the bare origin', async () => {
    const result = await release();
    assert.equal(result.nextRelease.version, '1.15.0-rc.1');
    assert.equal(result.nextRelease.gitTag, 'v1.15.0-rc.1');
    assert.equal(result.nextRelease.channel, 'rc');
    await assertTags(rc1Tags);
  });

  await t.test('existing rc.1 verifies against channel metadata fetched from the bare origin', async () => {
    await git('update-ref', '-d', 'refs/notes/semantic-release-v1.15.0-rc.1');
    const result = await verifyExistingTag('1.15.0-rc.1', feature);
    assert.equal(result.signal, null, result.stderr);
    assert.equal(result.status, 0, result.stderr);
    await assertTags(rc1Tags);
  });

  await t.test('rerunning a published commit does not release again', async () => {
    assert.equal(await release(), false);
    await assertTags(rc1Tags);
  });

  const fix = await commit('fix(DSX-1575): correct identity release candidate');
  await git('push', 'origin', 'release/1.15.0');
  const rc2Tags = { ...rc1Tags, 'v1.15.0-rc.2': fix };
  await t.test('a subsequent fix publishes rc.2 on the same version base', async () => {
    const result = await release();
    assert.equal(result.lastRelease.version, '1.15.0-rc.1');
    assert.equal(result.nextRelease.version, '1.15.0-rc.2');
    assert.equal(result.nextRelease.gitTag, 'v1.15.0-rc.2');
    assert.equal(result.nextRelease.channel, 'rc');
    assert.equal(result.nextRelease.type, 'patch');
    assert.equal(result.nextRelease.gitHead, fix);
    assert.match(result.nextRelease.notes, /correct identity release candidate/);
    assert.doesNotMatch(result.nextRelease.notes, /add identity release candidate/);
    await assertTags(rc2Tags);
  });

  await t.test('docs after rc.2 do not release or move any existing tag', async () => {
    await commit('docs: clarify release candidate usage');
    await git('push', 'origin', 'release/1.15.0');
    assert.equal(await release(), false);
    await assertTags(rc2Tags);
    assert.equal(await remoteGit('rev-parse', 'refs/heads/master'), master);
  });

  await t.test('a breaking feature is blocked by the real verifyRelease hook before tagging', async () => {
    await commit('feat(DSX-1575)!: break identity compatibility');
    await git('push', 'origin', 'release/1.15.0');
    const result = await runRelease();
    assert.equal(result.signal, null, result.stderr);
    assert.equal(result.status, 1, result.stderr);
    const { errors } = JSON.parse(result.stdout);
    assert.deepEqual(errors, [{
      message: 'Calculated RC version does not match the release branch target',
      pluginName: './.github/rc/validate-version.cjs',
    }]);
    await assertTags(rc2Tags);
    assert.equal(await remoteGit('rev-parse', 'refs/heads/master'), master);
  });

  await t.test('a matching-head manual RC tag without release metadata fails closed without tag changes', async () => {
    const head = await git('rev-parse', 'HEAD');
    await git('tag', 'v1.15.0-rc.3');
    await git('push', 'origin', 'refs/tags/v1.15.0-rc.3');
    const manualTags = { ...rc2Tags, 'v1.15.0-rc.3': head };
    await assertTags(manualTags);
    const result = await verifyExistingTag('1.15.0-rc.3', head);
    assert.equal(result.signal, null, result.stderr);
    assert.equal(result.status, 1, result.stderr);
    await assertTags(manualTags);
    assert.equal(await remoteGit('rev-parse', 'refs/heads/master'), master);
  });
});
