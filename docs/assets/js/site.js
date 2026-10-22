// 首页与文档共用：本机静态搜索、复制按钮。无第三方依赖。
const dialog = document.querySelector('[data-search-dialog]');
const input = dialog?.querySelector('[data-search-input]');
const list = dialog?.querySelector('[data-search-results]');
let index = null;
let indexPromise = null;
let results = [];
let active = -1;
let lastFocus = null;

function escapeHTML(text) {
  return text.replace(/[&<>"']/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
}

function pages() {
  try { return JSON.parse(document.getElementById('search-pages')?.textContent || '[]').filter(Boolean); }
  catch { return []; }
}

// 按需抓取文档页面并按 h2/h3 切分为段落；索引只存在于当前标签页。
async function buildIndex() {
  const key = `psdkit-search:${document.documentElement.dataset.build || ''}`;
  try {
    const cached = sessionStorage.getItem(key);
    if (cached) return JSON.parse(cached);
  } catch { /* 存储不可用时直接重建 */ }
  const parser = new DOMParser();
  const entries = [];
  await Promise.all(pages().map(async (page, order) => {
    try {
      const response = await fetch(page.url, { credentials: 'same-origin' });
      if (!response.ok) return;
      const html = parser.parseFromString(await response.text(), 'text/html');
      const article = html.querySelector('.doc-content');
      if (!article) return;
      let current = { page: page.title, section: page.section, heading: '', url: page.url, text: '', order, depth: 0 };
      const push = () => { current.text = current.text.replace(/\s+/g, ' ').trim(); if (current.text || current.heading) entries.push(current); };
      for (const node of article.children) {
        if (/^H[23]$/.test(node.tagName) && node.id) {
          push();
          current = { page: page.title, section: page.section, heading: node.textContent.replace('#', '').trim(), url: `${page.url}#${encodeURIComponent(node.id)}`, text: '', order, depth: node.tagName === 'H2' ? 1 : 2 };
        } else if (node.tagName !== 'H1') {
          current.text += ` ${node.textContent}`;
        }
      }
      push();
    } catch { /* 单页失败不影响其他结果 */ }
  }));
  entries.sort((a, b) => a.order - b.order);
  try { sessionStorage.setItem(key, JSON.stringify(entries)); } catch { /* 忽略配额 */ }
  return entries;
}

function score(entry, terms) {
  let total = 0;
  const page = entry.page.toLowerCase();
  const heading = entry.heading.toLowerCase();
  const text = entry.text.toLowerCase();
  for (const term of terms) {
    let hit = 0;
    if (page.includes(term)) hit += entry.depth === 0 ? 12 : 3;
    if (heading.includes(term)) hit += 9;
    if (text.includes(term)) hit += 1 + Math.min(3, text.split(term).length - 2);
    if (!hit) return 0;
    total += hit;
  }
  return total;
}

function highlight(text, terms) {
  let html = escapeHTML(text);
  for (const term of terms) {
    if (!term) continue;
    const pattern = new RegExp(escapeHTML(term).replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi');
    html = html.replace(pattern, (match) => `<mark>${match}</mark>`);
  }
  return html;
}

function snippet(text, terms) {
  const lower = text.toLowerCase();
  const at = Math.max(0, Math.min(...terms.map((term) => { const found = lower.indexOf(term); return found < 0 ? Infinity : found; })));
  if (!Number.isFinite(at)) return text.slice(0, 90);
  const start = Math.max(0, at - 24);
  return `${start > 0 ? '…' : ''}${text.slice(start, start + 110)}`;
}

function render(query) {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  active = -1;
  input.setAttribute('aria-activedescendant', '');
  if (!terms.length) { list.innerHTML = ''; results = []; return; }
  if (!index) { list.innerHTML = '<li class="search-dialog__empty">正在建立本机索引…</li>'; return; }
  results = index.map((entry) => ({ entry, value: score(entry, terms) })).filter((item) => item.value > 0)
    .sort((a, b) => b.value - a.value || a.entry.order - b.entry.order).slice(0, 24).map((item) => item.entry);
  if (!results.length) { list.innerHTML = `<li class="search-dialog__empty">没有找到 “${escapeHTML(query)}”。试试 API 名称，例如 OpenEditor。</li>`; return; }
  list.innerHTML = results.map((entry, position) => `
    <li class="search-result" role="option" id="search-option-${position}" aria-selected="false">
      <a href="${entry.url}">
        <span class="search-result__path">${escapeHTML(entry.section)} / ${escapeHTML(entry.page)}</span>
        <span class="search-result__title">${highlight(entry.heading || entry.page, terms)}</span>
        <span class="search-result__snippet">${highlight(snippet(entry.text, terms), terms)}</span>
      </a>
    </li>`).join('');
  select(0);
}

function select(position) {
  const options = [...list.querySelectorAll('.search-result')];
  if (!options.length) return;
  active = (position + options.length) % options.length;
  options.forEach((option, number) => option.setAttribute('aria-selected', String(number === active)));
  input.setAttribute('aria-activedescendant', options[active].id);
  options[active].scrollIntoView({ block: 'nearest' });
}

export function openSearch() {
  if (!dialog) return;
  lastFocus = document.activeElement;
  dialog.hidden = false;
  document.documentElement.style.overflow = 'hidden';
  input.focus();
  input.select();
  if (!indexPromise) indexPromise = buildIndex().then((entries) => { index = entries; render(input.value); });
}

function closeSearch() {
  if (!dialog || dialog.hidden) return;
  dialog.hidden = true;
  document.documentElement.style.overflow = '';
  lastFocus?.focus?.();
}

if (dialog) {
  document.querySelectorAll('[data-search-open]').forEach((button) => button.addEventListener('click', openSearch));
  dialog.querySelectorAll('[data-search-close]').forEach((button) => button.addEventListener('click', closeSearch));
  input.addEventListener('input', () => render(input.value.trim()));
  input.addEventListener('keydown', (event) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); select(active + 1); }
    else if (event.key === 'ArrowUp') { event.preventDefault(); select(active - 1); }
    else if (event.key === 'Enter' && results[active]) { event.preventDefault(); closeSearch(); location.href = results[active].url; }
  });
  dialog.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') { event.preventDefault(); closeSearch(); }
    if (event.key === 'Tab') { event.preventDefault(); input.focus(); }
  });
  document.addEventListener('keydown', (event) => {
    const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName) || document.activeElement?.isContentEditable;
    if ((event.key === 'k' && (event.metaKey || event.ctrlKey)) || (event.key === '/' && !typing)) {
      event.preventDefault();
      if (dialog.hidden) openSearch(); else closeSearch();
    }
  });
}

// 通用复制按钮：<button data-copy="文本"> 或 data-copy-target="#id"。
export async function copyText(text) {
  try { await navigator.clipboard.writeText(text); return true; }
  catch {
    const area = Object.assign(document.createElement('textarea'), { value: text });
    area.setAttribute('readonly', '');
    area.style.cssText = 'position:fixed;opacity:0';
    document.body.append(area);
    area.select();
    const done = document.execCommand('copy');
    area.remove();
    return done;
  }
}

export function flashCopied(button, label = '已复制') {
  const original = button.dataset.label ?? button.getAttribute('aria-label') ?? '';
  button.dataset.label = original;
  button.classList.add('is-copied');
  const text = button.querySelector('[data-copy-label]');
  if (text) { text.dataset.original ??= text.textContent; text.textContent = label; }
  button.setAttribute('aria-label', label);
  clearTimeout(button._copyTimer);
  button._copyTimer = setTimeout(() => {
    button.classList.remove('is-copied');
    if (text) text.textContent = text.dataset.original;
    button.setAttribute('aria-label', original);
  }, 1600);
}

document.querySelectorAll('[data-copy], [data-copy-target]').forEach((button) => {
  button.addEventListener('click', async () => {
    const text = button.dataset.copy ?? document.querySelector(button.dataset.copyTarget)?.textContent ?? '';
    if (await copyText(text.trim())) flashCopied(button);
  });
});
