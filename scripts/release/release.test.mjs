import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { modulePath, parseVersion, compareVersions, previousVersion } from './version.mjs';
import { collectNotes, inspectRelease, publishRelease, renderNotes, verifyExpected, verifyRemoteTag } from './release.mjs';
import { consumerEnvironment, retryProxy, verifyModule } from './consumer.mjs';

const metadata = { tag: 'v0.2.0', sha: 'a'.repeat(40), tagObject: 'b'.repeat(40),
  goVersion: '1.27.3', previous: 'v0.1.0', prerelease: false };

test('只接受 Go 可引用的完整版本和合法预发布版本', () => {
  for (const tag of ['v0.1.0', 'v1.0.0', 'v0.2.0-rc.1', 'v0.2.0-beta-1']) assert.equal(parseVersion(tag).tag, tag);
  for (const tag of ['0.1.0', 'v0.1', 'v01.2.3', 'v0.1.0-01', 'v0.1.0+', 'v0.1.0+meta', 'v0.1.0\n', 'v0.1.0\nBAD=1']) {
    assert.throws(() => parseVersion(tag), /无效版本/);
  }
  const versions = ['v0.9.0', 'v0.10.0-alpha.2', 'v0.10.0-alpha.10', 'v0.10.0-beta', 'v0.10.0-rc.1', 'v0.10.0'];
  assert.deepEqual([...versions].reverse().sort(compareVersions), versions);
  assert.equal(compareVersions('v0.1.0-rc.1', 'v0.1.0-rc.1'), 0);
  assert.equal(compareVersions('v0.1.0', 'v0.1.0'), 0);
});

test('正式版本说明覆盖整个预发布周期，预发布按上一个预发布增量生成', () => {
  const tags = ['v0.9.0', 'v0.10.0-rc.1', 'v0.10.0-rc.2', 'v99.0.0', 'invalid'];
  assert.equal(previousVersion('v0.10.0', tags), 'v0.9.0');
  assert.equal(previousVersion('v0.10.0-rc.3', tags), 'v0.10.0-rc.2');
  assert.equal(previousVersion('v0.1.0', tags), '');
});

test('中文说明含准确安装版本、变更分类、破坏性变更和安全的原始提交标题', () => {
  const commits = [
    { sha: '1'.repeat(40), subject: 'feat(render): 增加缓存' },
    { sha: '2'.repeat(40), subject: 'fix: 修复 <script> 和 [链接](https://bad)' },
    { sha: '3'.repeat(40), subject: 'refactor(api)!: 调整调用参数' },
    { sha: '4'.repeat(40), subject: 'feat: 调整结构', body: 'BREAKING CHANGE: 参数有变化' },
  ];
  const notes = renderNotes(metadata, commits, '支持中文手写说明。', 'https://github.com/example/run');
  for (const text of ['### 新增功能', '### 问题修复', '### 兼容性变更',
    `go get ${modulePath}@v0.2.0`, '/compare/v0.1.0...v0.2.0', '支持中文手写说明。', 'proxy.golang.org']) {
    assert.ok(notes.includes(text), text);
  }
  assert.ok(!notes.includes('<script>'));
  assert.ok(!notes.includes('[链接](https://bad)'));
  assert.ok(renderNotes({ ...metadata, previous: '', prerelease: true }, []).includes('预发布'));
});

test('真实 Git 仓库检查注释 Tag、main 归属、版本增量、模块路径及远端 Tag 移动', t => {
  const directory = mkdtempSync(join(tmpdir(), 'psdkit-release-git-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const git = (...args) => execFileSync('git', args, { cwd: directory, encoding: 'utf8', stdio: 'pipe' }).trim();
  git('init', '--initial-branch=main');
  git('config', 'user.name', 'Release Test');
  git('config', 'user.email', 'release@example.invalid');
  git('remote', 'add', 'origin', directory);
  writeFileSync(join(directory, 'go.mod'), `module ${modulePath}\n\ngo 1.27.3\n`);
  git('add', 'go.mod');
  git('commit', '-m', 'feat: 首个版本');
  git('tag', '-a', 'v0.1.0', '-m', 'first');
  git('commit', '--allow-empty', '-m', 'fix: 中文修复');
  git('tag', '-a', 'v0.2.0', '-m', 'next');
  git('update-ref', 'refs/remotes/origin/main', 'HEAD');
  const release = inspectRelease('v0.2.0', git);
  assert.equal(release.previous, 'v0.1.0');
  assert.notEqual(release.tagObject, release.sha);
  verifyRemoteTag(release, git);
  const notes = collectNotes(release, git, {});
  assert.ok(notes.includes('中文修复'));
  assert.ok(!notes.includes('首个版本'));
  git('tag', '-f', 'v0.2.0', 'HEAD^');
  assert.throws(() => verifyRemoteTag(release, git), /远端 Tag 已变化/);
  git('checkout', '-b', 'unmerged');
  git('commit', '--allow-empty', '-m', 'feat: 未合并');
  git('tag', 'v0.3.0');
  assert.throws(() => inspectRelease('v0.3.0', git));
  assert.throws(() => inspectRelease('v2.0.0', git), /迁移模块路径/);
  git('checkout', 'main');
  writeFileSync(join(directory, 'go.mod'), 'module example.com/wrong\n\ngo 1.27.3\n');
  git('add', 'go.mod');
  git('commit', '-m', 'build: 错误模块名');
  git('tag', 'v0.4.0');
  git('update-ref', 'refs/remotes/origin/main', 'HEAD');
  assert.throws(() => inspectRelease('v0.4.0', git), /必须声明/);
});

test('拒绝变更后的提交、Tag 对象、错误仓库和错误 push 来源', () => {
  verifyExpected(metadata, { RELEASE_SHA: metadata.sha, RELEASE_TAG_OBJECT: metadata.tagObject });
  for (const env of [{ RELEASE_SHA: 'changed' }, { RELEASE_TAG_OBJECT: 'changed' },
    { GITHUB_REPOSITORY: 'other/repo' }, { GITHUB_EVENT_NAME: 'push', GITHUB_SHA: metadata.sha, GITHUB_REF: 'refs/heads/main' }]) {
    assert.throws(() => verifyExpected(metadata, env));
  }
});

test('发布幂等：已发布版本不写入，首次创建和草稿恢复正确处理预发布标记', async () => {
  const existing = { id: 1, tag_name: metadata.tag, draft: false, html_url: 'https://example.invalid/release' };
  const calls = [];
  await publishRelease(metadata, 'notes', async (...args) => { calls.push(args); return existing; });
  assert.equal(calls.length, 1);
  for (const draft of [null, { ...existing, draft: true }]) {
    const requests = [];
    await publishRelease({ ...metadata, prerelease: true }, '中文发布说明', async (path, options) => {
      requests.push([path, options]);
      return options ? { ...existing, prerelease: true } : draft;
    });
    assert.equal(requests[1][1].method, draft ? 'PATCH' : 'POST');
    assert.equal(requests[1][1].body.target_commitish, metadata.sha);
    assert.equal(requests[1][1].body.prerelease, true);
    assert.equal(requests[1][1].body.make_latest, 'false');
    assert.equal(requests[1][1].body.body, '中文发布说明');
  }
});

test('公共代理短暂不可见会重试，达到上限必须失败', async () => {
  let attempts = 0;
  const pauses = [];
  const result = await retryProxy(async () => {
    if (++attempts < 3) throw new Error('not available');
    return 'downloaded';
  }, async ms => pauses.push(ms), () => {});
  assert.equal(result, 'downloaded');
  assert.deepEqual(pauses, [15_000, 30_000]);
  attempts = 0;
  await assert.rejects(retryProxy(async () => { attempts++; throw new Error('offline'); }, async () => {}, () => {}), /offline/);
  assert.equal(attempts, 8);
});

test('隔离本地 Go 配置并拒绝错误版本、replace、缺失校验和或代理提交不符', () => {
  const env = consumerEnvironment('/tmp/consumer', { GOFLAGS: '-mod=vendor', GOWORK: '/local/go.work', GOPROXY: 'direct' });
  assert.equal(env.GOWORK, 'off');
  assert.equal(env.GOFLAGS, '-modcacherw');
  assert.equal(env.GOPROXY, 'https://proxy.golang.org');
  const dependency = { Path: modulePath, Version: metadata.tag, Sum: 'h1:source', GoModSum: 'h1:mod', Origin: { Hash: metadata.sha } };
  verifyModule(dependency, metadata.tag, metadata.sha);
  for (const patch of [{ Version: 'v0.1.0' }, { Replace: {} }, { Sum: undefined }, { Origin: { Hash: 'wrong' } }]) {
    assert.throws(() => verifyModule({ ...dependency, ...patch }, metadata.tag, metadata.sha));
  }
});
