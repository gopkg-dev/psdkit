import { execFileSync } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { modulePath, repository, parseVersion, previousVersion } from './version.mjs';

export function git(...args) {
  return execFileSync('git', args, { encoding: 'utf8', timeout: 120_000, maxBuffer: 16 * 1024 * 1024 }).trim();
}

export function inspectRelease(tag, run = git) {
  const version = parseVersion(tag);
  if (version.numbers[0] > 1n) throw new Error('当前模块路径仅支持 v0/v1；v2 及以后需要先迁移模块路径和引用验证程序。');
  const ref = `refs/tags/${tag}`;
  const sha = run('rev-parse', '--verify', `${ref}^{commit}`);
  const tagObject = run('rev-parse', '--verify', ref);
  run('merge-base', '--is-ancestor', sha, 'refs/remotes/origin/main');
  const goMod = run('show', `${sha}:go.mod`);
  if (!new RegExp(`^module ${modulePath.replaceAll('.', '\\.')}\\s*$`, 'm').test(goMod)) {
    throw new Error(`Tag 的 go.mod 必须声明 ${modulePath}。`);
  }
  const goVersion = /^go (\d+\.\d+\.\d+)\s*$/m.exec(goMod)?.[1];
  if (!goVersion) throw new Error('Tag 的 go.mod 缺少完整 Go 工具链版本。');
  const tags = run('tag', '--merged', sha).split('\n').filter(Boolean);
  return { tag, sha, tagObject, goVersion, previous: previousVersion(tag, tags),
    prerelease: version.prerelease.length > 0 };
}

export function verifyRemoteTag(metadata, run = git) {
  const ref = `refs/tags/${metadata.tag}`;
  const lines = run('ls-remote', '--exit-code', 'origin', ref, `${ref}^{}`).split('\n');
  const refs = new Map(lines.map(line => line.trim().split(/\s+/)).map(([sha, name]) => [name, sha]));
  const object = refs.get(ref);
  const sha = refs.get(`${ref}^{}`) ?? object;
  if (object !== metadata.tagObject || sha !== metadata.sha) {
    throw new Error('远端 Tag 已变化或被删除；停止发布，不能覆盖已经验证的版本。');
  }
}

export function verifyExpected(metadata, env = process.env) {
  if (env.GITHUB_REPOSITORY && env.GITHUB_REPOSITORY !== repository) {
    throw new Error(`发布流程只适用于 ${repository}。`);
  }
  if (env.RELEASE_SHA && env.RELEASE_SHA !== metadata.sha ||
      env.RELEASE_TAG_OBJECT && env.RELEASE_TAG_OBJECT !== metadata.tagObject) {
    throw new Error('Tag 与准备阶段的提交不一致。');
  }
  if (env.GITHUB_EVENT_NAME === 'push' &&
      (env.GITHUB_SHA !== metadata.sha || env.GITHUB_REF !== `refs/tags/${metadata.tag}`)) {
    throw new Error('Tag 与触发本次工作流的提交不一致。');
  }
}

function markdown(text) {
  return text.replace(/[&<>]/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[char])
    .replace(/[\\`*_\[\]~]/g, '\\$&');
}

export function renderNotes(metadata, commits, curated = '', runURL = '') {
  const headings = { feat: '新增功能', fix: '问题修复', perf: '性能改进', docs: '文档与示例',
    test: '测试', refactor: '代码整理', build: '构建与依赖', ci: '自动化与发布', chore: '维护' };
  const groups = new Map();
  for (const { sha, subject, body = '' } of commits) {
    const match = /^(\w+)(?:\(([^)]+)\))?(!)?:\s*(.+)$/.exec(subject);
    const heading = match?.[3] || /BREAKING[ -]CHANGE:/i.test(body) ? '兼容性变更' :
      headings[match?.[1]] ?? '其他更新';
    const message = match ? `${match[2] ? `${match[2]}：` : ''}${match[4]}` : subject;
    if (!groups.has(heading)) groups.set(heading, []);
    groups.get(heading).push(`- ${markdown(message)}（[${sha.slice(0, 7)}](https://github.com/${repository}/commit/${sha})）`);
  }
  const changes = [...groups].map(([heading, entries]) => `### ${heading}\n\n${entries.join('\n')}`).join('\n\n');
  const comparison = metadata.previous ?
    `[查看 ${metadata.previous} → ${metadata.tag} 的完整差异](https://github.com/${repository}/compare/${metadata.previous}...${metadata.tag})` :
    `[查看源码](https://github.com/${repository}/tree/${metadata.tag})`;
  return `## ${metadata.tag}${metadata.prerelease ? ' 预发布' : ''}\n\n` +
    `psdkit 是用于读取、编辑和合成 PSD/PSB 的 Go 库，提供浏览器 WebAssembly 演示。\n\n` +
    `${curated.trim() ? `${curated.trim()}\n\n` : ''}` +
    `## 安装\n\n需要 Go **${metadata.goVersion}** 或更新版本。\n\n` +
    `\`\`\`sh\ngo get ${modulePath}@${metadata.tag}\n\`\`\`\n\n` +
    `## 本次更新\n\n${changes || '此版本没有新增提交，版本与发布配置以对应 Tag 为准。'}\n\n${comparison}\n\n` +
    `## 验证\n\n` +
    `- Linux、macOS、Windows：Go 测试、竞态检测和 vet。\n` +
    `- golangci-lint、格式检查、浏览器 SDK 测试、Wasm/Playground 构建与仓库检查。\n` +
    `- 独立 Go 项目经 \`proxy.golang.org\` 下载此版本并通过校验和验证，不使用本地 replace。\n` +
    `- 实际运行 PSD/PSB 创建、保存重开、重复原位保存、图层组合成与像素检查。\n` +
    `${runURL ? `\n[查看本次检查记录](${runURL})\n` : ''}\n` +
    `## 文档与支持范围\n\n` +
    `- [使用文档](https://psdkit.asin1.com/guide/) · [在线 Playground](https://psdkit.asin1.com/demo/)\n` +
    `- [此版本能力矩阵与已知限制](https://github.com/${repository}/blob/${metadata.tag}/docs/status.md)\n` +
    `${metadata.tag.startsWith('v0.') ? '- v0.x 的公共 API 仍可能调整。\n' : ''}` +
    `- 不支持的特性和显式近似以此版本能力矩阵为准。\n` +
    `- MIT 许可。作为 Go 库发布，源码归档由 GitHub 提供，无需单独下载平台二进制。\n`;
}

export function collectNotes(metadata, run = git, env = process.env) {
  const range = metadata.previous ? `${metadata.previous}..${metadata.sha}` : metadata.sha;
  const records = run('log', '--reverse', '--no-merges', '--format=%H%x00%s%x00%b%x00', range).split('\0');
  const commits = [];
  for (let index = 0; index + 2 < records.length; index += 3) {
    commits.push({ sha: records[index].trim(), subject: records[index + 1], body: records[index + 2] });
  }
  const notePath = `docs/releases/${metadata.tag}.md`;
  const paths = run('ls-tree', '--name-only', metadata.sha, '--', notePath);
  const curated = paths ? run('show', `${metadata.sha}:${notePath}`) : '';
  const runURL = env.GITHUB_RUN_ID ? `https://github.com/${repository}/actions/runs/${env.GITHUB_RUN_ID}` : '';
  return renderNotes(metadata, commits, curated, runURL);
}

export async function github(path, { method = 'GET', body } = {}) {
  if (!process.env.GH_TOKEN) throw new Error('发布需要 GH_TOKEN（Actions 中使用内置 github.token）。');
  const response = await fetch(`https://api.github.com/repos/${repository}/${path}`, {
    method, signal: AbortSignal.timeout(30_000),
    headers: { Accept: 'application/vnd.github+json', Authorization: `Bearer ${process.env.GH_TOKEN}`,
      'X-GitHub-Api-Version': '2022-11-28', 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (response.status === 404 && method === 'GET') return null;
  if (!response.ok) throw new Error(`GitHub ${method} ${path} 失败（HTTP ${response.status}）。`);
  return response.json();
}

export async function publishRelease(metadata, notes, api = github) {
  const existing = await api(`releases/tags/${encodeURIComponent(metadata.tag)}`);
  if (existing && !existing.draft) {
    console.log(`已发布，保留原说明：${existing.html_url}`);
    return existing;
  }
  const body = { tag_name: metadata.tag, target_commitish: metadata.sha,
    name: `${metadata.tag} · ${metadata.prerelease ? '预发布' : '版本更新'}`,
    body: notes, draft: false, prerelease: metadata.prerelease,
    make_latest: metadata.prerelease ? 'false' : 'legacy' };
  const release = await api(existing ? `releases/${existing.id}` : 'releases', {
    method: existing ? 'PATCH' : 'POST', body,
  });
  if (release.draft || release.tag_name !== metadata.tag) throw new Error('GitHub 返回了不符合预期的发布状态。');
  return release;
}

function summary(text) {
  console.log(text);
  if (process.env.GITHUB_STEP_SUMMARY) appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${text}\n`);
}

async function main(mode) {
  if (!['prepare', 'notes', 'publish'].includes(mode)) throw new Error('用法：release.mjs prepare|notes|publish');
  const metadata = inspectRelease(process.env.RELEASE_TAG);
  verifyExpected(metadata);
  verifyRemoteTag(metadata);
  if (mode === 'prepare') {
    if (process.env.GITHUB_OUTPUT) {
      appendFileSync(process.env.GITHUB_OUTPUT,
        `tag=${metadata.tag}\nsha=${metadata.sha}\ntag-object=${metadata.tagObject}\ngo-version=${metadata.goVersion}\n`);
    }
    summary(`版本：${metadata.tag}；提交：${metadata.sha}；Go：${metadata.goVersion}。`);
    return;
  }
  const notes = collectNotes(metadata);
  if (mode === 'notes') { summary(notes); return; }
  if (process.env.GITHUB_EVENT_NAME !== 'push') throw new Error('只有版本 Tag 的 push 事件可以发布；手动入口只做验证。');
  const release = await publishRelease(metadata, notes);
  summary(`发布完成：[${metadata.tag}](${release.html_url})`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv[2]).catch(error => { console.error(error.message); process.exitCode = 1; });
}
