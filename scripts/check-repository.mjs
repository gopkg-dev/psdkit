import { execFileSync } from 'node:child_process';
import { readFile, stat } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const files = [...new Set(execFileSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard'],
  { cwd: root, encoding: 'utf8' }).split('\0').filter(Boolean))];
const failures = [];
for (const file of files) {
  const path = resolve(root, file);
  const info = await stat(path);
  if (info.size > 1024 * 1024) failures.push(`${file}: over 1 MiB; move external assets to the corpus`);
  if (file.endsWith('.md')) {
    // 代码块中的 Go 表达式（例如 errors.AsType[T](err)）不是 Markdown 链接。
    const text = (await readFile(path, 'utf8')).replace(/^```[\s\S]*?^```/gm, '');
    for (const match of text.matchAll(/\]\(([^\s)]+)\)/g)) {
      const target = match[1].split('#')[0];
      if (!target || /^(?:[a-z]+:|\/)/i.test(target)) continue;
      // Playground 由 Pages 工作流构建到站点的 demo/ 下，文档中的相对链接在源码树中不存在。
      if (file.startsWith('docs/') && /^(?:\.\.\/)*demo\/$/.test(target)) continue;
      try { await stat(resolve(dirname(path), decodeURIComponent(target))); }
      catch { failures.push(`${file}: missing local link ${target}`); }
    }
  }
}
if (failures.length) {
  console.error(failures.join('\n'));
  process.exitCode = 1;
} else {
  console.log(`Repository hygiene passed (${files.length} files, no oversized assets or broken local Markdown links).`);
}
