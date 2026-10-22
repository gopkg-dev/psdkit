import { execFile } from 'node:child_process';
import { appendFile, copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { promisify } from 'node:util';
import { pathToFileURL } from 'node:url';
import { modulePath, parseVersion } from './version.mjs';

const execute = promisify(execFile);

export function consumerEnvironment(directory, inherited = process.env) {
  return { ...inherited, GOENV: 'off', GOWORK: 'off', GOFLAGS: '-modcacherw', GOTOOLCHAIN: 'local',
    GOPROXY: 'https://proxy.golang.org', GOSUMDB: 'sum.golang.org',
    GOPRIVATE: '', GONOPROXY: 'none', GONOSUMDB: 'none',
    GOMODCACHE: join(directory, 'modcache'), GOCACHE: join(directory, 'buildcache') };
}

export async function retryProxy(operation, sleep = delay, report = console.log) {
  for (let attempt = 1; attempt <= 8; attempt++) {
    try { return await operation(); }
    catch (error) {
      if (attempt === 8) throw error;
      const seconds = Math.min(attempt * 15, 60);
      report(`公共代理尚未就绪（第 ${attempt}/8 次）；${seconds} 秒后重试。\n${error.stderr ?? error.message}`);
      await sleep(seconds * 1000);
    }
  }
}

export function verifyModule(dependency, tag, sha) {
  if (dependency.Path !== modulePath || dependency.Version !== tag || dependency.Replace) {
    throw new Error('独立项目没有引用预期的公开版本，或包含 replace。');
  }
  if (!dependency.Sum?.startsWith('h1:') || !dependency.GoModSum?.startsWith('h1:')) {
    throw new Error('公开模块缺少源码或 go.mod 校验和。');
  }
  if (sha && dependency.Origin?.Hash && dependency.Origin.Hash !== sha) {
    throw new Error('公共代理中的提交与 Tag 不一致。');
  }
}

export async function verifyConsumer(tag, sha = '') {
  parseVersion(tag);
  const directory = await mkdtemp(join(tmpdir(), 'psdkit-release-consumer-'));
  try {
    const project = join(directory, 'project');
    await mkdir(project);
    await copyFile(new URL('./consumer/main.go', import.meta.url), join(project, 'main.go'));
    const options = { cwd: project, env: consumerEnvironment(directory), maxBuffer: 4 * 1024 * 1024 };
    const go = async (...args) => (await execute('go', args,
      { ...options, timeout: args[0] === 'get' ? 60_000 : 180_000 })).stdout.trim();
    await writeFile(join(project, 'go.mod'), 'module example.com/psdkit-release-consumer\n');
    console.log(`独立目录：${project}\n安装：go get ${modulePath}@${tag}（公共代理，无本地回退）`);
    await retryProxy(() => go('get', `${modulePath}@${tag}`));
    const dependency = JSON.parse(await go('mod', 'download', '-json', `${modulePath}@${tag}`));
    verifyModule(dependency, tag, sha);
    const modFile = JSON.parse(await go('mod', 'edit', '-json'));
    if (modFile.Replace?.length) throw new Error('独立项目包含 replace。');
    const sums = await readFile(join(project, 'go.sum'), 'utf8');
    if (!sums.includes(`${modulePath} ${tag} ${dependency.Sum}`)) throw new Error('go.sum 未记录目标版本的源码校验和。');
    console.log(await go('mod', 'verify'));
    const result = await go('run', '.', '-expected', tag);
    console.log(result);
    const summary = `## 独立项目引用验证通过\n\n` +
      `- 版本：\`${modulePath}@${tag}\`。\n` +
      `- 来源：\`proxy.golang.org\`；\`sum.golang.org\` 校验；全新模块与缓存，无 replace。\n` +
      `- 源码校验和：\`${dependency.Sum}\`。\n` +
      `- 已运行 PSD/PSB 保存重开、重复原位保存、图层组合成与像素检查。\n`;
    if (process.env.GITHUB_STEP_SUMMARY) await appendFile(process.env.GITHUB_STEP_SUMMARY, summary);
    return dependency;
  } finally { await rm(directory, { recursive: true, force: true }); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  verifyConsumer(process.env.RELEASE_TAG, process.env.RELEASE_SHA).catch(error => {
    console.error(error.stderr ?? error.message);
    process.exitCode = 1;
  });
}
