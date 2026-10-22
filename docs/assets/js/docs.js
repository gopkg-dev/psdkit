// 文档页面增强：导航抽屉、主题、页内目录、标题锚点、代码复制、提示块与表格。
import { copyText, flashCopied } from './site.js';

const root = document.documentElement;
const article = document.querySelector('.doc-content');

// 主题切换（默认跟随系统，选择后记住）。
document.querySelector('[data-theme-toggle]')?.addEventListener('click', () => {
  const next = root.dataset.theme === 'light' ? 'dark' : 'light';
  root.dataset.theme = next;
  try { localStorage.setItem('psdkit-theme', next); } catch { /* 隐私模式下仅本次有效 */ }
});

// 移动端导航抽屉。
const sidebar = document.getElementById('docs-sidebar');
const scrim = document.querySelector('.docs-sidebar__scrim');
const toggle = document.querySelector('.sidebar-toggle');
function setSidebar(open) {
  if (!sidebar) return;
  sidebar.classList.toggle('is-open', open);
  scrim.hidden = !open;
  toggle?.setAttribute('aria-expanded', String(open));
  root.style.overflow = open ? 'hidden' : '';
  if (open) sidebar.querySelector('.is-active, a, button')?.focus();
  else toggle?.focus({ preventScroll: true });
}
toggle?.addEventListener('click', () => setSidebar(true));
document.querySelectorAll('[data-sidebar-close]').forEach((element) => element.addEventListener('click', () => setSidebar(false)));
document.addEventListener('keydown', (event) => { if (event.key === 'Escape' && sidebar?.classList.contains('is-open')) setSidebar(false); });
matchMedia('(min-width: 961px)').addEventListener('change', (event) => { if (event.matches && sidebar?.classList.contains('is-open')) setSidebar(false); });
sidebar?.querySelector('.is-active')?.scrollIntoView({ block: 'center' });

if (article) {
  // GitHub 风格提示块：> [!NOTE] / [!TIP] / [!IMPORTANT] / [!WARNING] / [!CAUTION]
  const titles = { NOTE: '说明', TIP: '提示', IMPORTANT: '重要', WARNING: '注意', CAUTION: '限制' };
  const icons = { NOTE: 'info', TIP: 'check', IMPORTANT: 'spark', WARNING: 'warning', CAUTION: 'warning' };
  for (const quote of article.querySelectorAll('blockquote')) {
    const first = quote.querySelector('p');
    const match = first?.innerHTML.match(/^\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*(?:<br\s*\/?>)?\s*/i);
    if (!match) continue;
    const kind = match[1].toUpperCase();
    first.innerHTML = first.innerHTML.slice(match[0].length);
    if (!first.textContent.trim()) first.remove();
    quote.classList.add('callout', `callout--${kind.toLowerCase()}`);
    const title = document.createElement('p');
    title.className = 'callout__title';
    title.innerHTML = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${iconPath(icons[kind])}</svg>${titles[kind]}`;
    quote.prepend(title);
  }

  // 表格横向滚动容器。
  for (const table of article.querySelectorAll('table')) {
    if (table.parentElement.classList.contains('table-wrap') || table.closest('.matrix')) continue;
    const wrap = document.createElement('div');
    wrap.className = 'table-wrap';
    wrap.tabIndex = 0;
    wrap.setAttribute('role', 'region');
    wrap.setAttribute('aria-label', table.querySelector('th')?.textContent ? `表格：${table.querySelector('th').textContent}` : '表格');
    table.replaceWith(wrap);
    wrap.append(table);
  }

  // 代码块：语言标签与复制按钮。
  for (const block of article.querySelectorAll('div.highlighter-rouge')) {
    const language = [...block.classList].find((name) => name.startsWith('language-'))?.slice(9);
    if (language && language !== 'plaintext') block.dataset.lang = { sh: 'shell', js: 'javascript' }[language] || language;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'copy-btn';
    button.setAttribute('aria-label', '复制代码');
    button.innerHTML = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${iconPath('copy')}</svg><span data-copy-label>复制</span>`;
    button.addEventListener('click', async () => {
      if (await copyText(block.querySelector('code')?.innerText.replace(/\n$/, '') || '')) flashCopied(button);
    });
    block.append(button);
  }

  // 标题锚点与页内目录。
  const tocList = document.querySelector('[data-toc]');
  const headings = [...article.querySelectorAll('h2[id], h3[id]')];
  for (const heading of headings) {
    const anchor = document.createElement('a');
    anchor.className = 'heading-anchor';
    anchor.href = `#${encodeURIComponent(heading.id)}`;
    anchor.textContent = '#';
    anchor.setAttribute('aria-label', `链接到“${heading.textContent}”`);
    heading.prepend(anchor);
    if (tocList) {
      const item = document.createElement('li');
      item.className = heading.tagName === 'H3' ? 'toc-h3' : 'toc-h2';
      const link = document.createElement('a');
      link.href = anchor.href;
      link.textContent = heading.textContent.replace(/^#/, '');
      item.append(link);
      tocList.append(item);
    }
  }
  if (tocList && headings.length) {
    const links = new Map([...tocList.querySelectorAll('a')].map((link, position) => [headings[position], link]));
    let current = null;
    const observer = new IntersectionObserver((records) => {
      for (const record of records) record.target._visible = record.isIntersecting;
      const visible = headings.find((heading) => heading._visible);
      if (visible) current = visible;
      else {
        const above = headings.filter((heading) => heading.getBoundingClientRect().top < 120);
        current = above.at(-1) || headings[0];
      }
      links.forEach((link, heading) => link.classList.toggle('is-active', heading === current));
    }, { rootMargin: '-72px 0px -65% 0px' });
    headings.forEach((heading) => observer.observe(heading));
  }
}

function iconPath(name) {
  const paths = {
    info: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18Zm0 8v6m0-9v.01',
    check: 'm4 12 5 5L20 6',
    spark: 'M12 3v4m0 10v4M3 12h4m10 0h4M6 6l2.5 2.5m7 7L18 18M6 18l2.5-2.5m7-7L18 6',
    warning: 'M12 3 2 21h20L12 3Zm0 6v5m0 3v.01',
    copy: 'M8 8h12v12H8V8Zm-4 8V4h12',
  };
  return `<path d="${paths[name]}"/>`;
}
