import { normalizePrint } from './apparel-prints.js';
import { PrintSessions } from './print-sessions.js';

// 首页交互：图层装置（概念示意）、滚动叙事、真实 Go Wasm 预览（按需加载）。
const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)');
// ?motion=reduce 可在不改系统设置时验证降级效果。
const forcedReduce = new URLSearchParams(location.search).get('motion') === 'reduce';
const reduced = () => forcedReduce || reduceMotion.matches;
const syncMotion = () => { document.documentElement.dataset.motion = reduced() ? 'reduce' : 'full'; };
syncMotion();
reduceMotion.addEventListener('change', syncMotion);
const base = document.documentElement.dataset.base || '/';

/* ───────── 页眉 ───────── */
const header = document.querySelector('.site-header--home');
const onScrollHeader = () => header?.classList.toggle('is-scrolled', scrollY > 24);
onScrollHeader();
addEventListener('scroll', onScrollHeader, { passive: true });

/* ───────── 图层装置（纯 CSS 示意，不调用引擎） ───────── */
const rig = document.querySelector('[data-rig]');
if (rig) {
  const ids = { bg: 1, subject: 2, glow: 3, ridge: 4, title: 5 };
  const names = { bg: '背景', subject: '主体', glow: '光晕', ridge: '远山', title: '文字', canvas: '透明画布' };
  // 选区控制点的范围（相对 640×480 画布的百分比）。
  const bounds = {
    canvas: [0, 0, 100, 100], bg: [0, 0, 100, 100], glow: [0, 0, 100, 100],
    subject: [50, 9.17, 41.25, 55], ridge: [0, 51.25, 100, 48.75], title: [5.6, 8.5, 37, 23],
  };
  const state = { mode: 'explode', selected: 'subject', hidden: new Set(), opacity: { glow: 80 }, masked: true };
  const layers = Object.fromEntries([...rig.querySelectorAll('.art-layer')].map((el) => [el.dataset.layer, el]));
  const labels = Object.fromEntries([...rig.querySelectorAll('.rig-label')].map((el) => [el.dataset.for, el]));
  const selection = rig.querySelector('[data-rig-selection]');
  const code = rig.querySelector('[data-rig-code]');
  const slider = rig.querySelector('[data-opacity]');
  const output = rig.querySelector('[data-opacity-out]');
  const maskButton = rig.querySelector('[data-mask]');

  const say = (html) => {
    code.innerHTML = html;
    code.classList.remove('code-flash');
    void code.offsetWidth;
    code.classList.add('code-flash');
  };

  function render() {
    rig.dataset.mode = state.mode;
    rig.dataset.masked = String(state.masked);
    for (const [name, el] of Object.entries(layers)) {
      el.classList.toggle('is-hidden', state.hidden.has(name));
      el.classList.toggle('is-selected', name === state.selected);
      el.style.setProperty('--op', String((state.opacity[name] ?? 100) / 100));
      const label = labels[name];
      if (label) {
        label.classList.toggle('is-selected', name === state.selected);
        label.classList.toggle('is-hidden', state.hidden.has(name));
        const op = label.querySelector('[data-op-label]');
        if (op) op.textContent = `${state.opacity[name] ?? 100}%`;
      }
    }
    rig.querySelectorAll('[data-layer-row]').forEach((row) => row.classList.toggle('is-selected', row.dataset.layerRow === state.selected));
    rig.querySelectorAll('[data-eye]').forEach((button) => {
      const visible = !state.hidden.has(button.dataset.eye);
      button.setAttribute('aria-pressed', String(visible));
      button.setAttribute('aria-label', `${visible ? '隐藏' : '显示'} ${names[button.dataset.eye]}`);
    });
    rig.querySelectorAll('[data-rig-mode]').forEach((button) => button.setAttribute('aria-checked', String(button.dataset.rigMode === state.mode)));
    const [x, y, w, h] = bounds[state.selected] || bounds.canvas;
    selection.style.setProperty('--sx', `${x}%`);
    selection.style.setProperty('--sy', `${y}%`);
    selection.style.setProperty('--sw', `${w}%`);
    selection.style.setProperty('--sh', `${h}%`);
    selection.style.setProperty('--sz', String(Number(layers[state.selected]?.style.getPropertyValue('--i') || 0)));
    selection.hidden = state.hidden.has(state.selected);
    const value = state.opacity[state.selected] ?? 100;
    slider.value = String(value);
    slider.disabled = state.selected === 'canvas';
    output.textContent = `${value}%`;
    maskButton.setAttribute('aria-pressed', String(state.masked));
  }

  function select(name) {
    if (!layers[name]) return;
    state.selected = name;
    render();
    if (ids[name]) say(`layer, _ := editor.<span class="tok-f">Layer</span>(<span class="tok-n">${ids[name]}</span>) <span class="tok-c">// ${names[name]}：Bounds、Opacity、Visible…</span>`);
  }

  rig.querySelectorAll('[data-rig-mode]').forEach((button) => button.addEventListener('click', () => {
    state.mode = button.dataset.rigMode;
    render();
    say(state.mode === 'explode'
      ? `tree, _ := editor.<span class="tok-f">Tree</span>() <span class="tok-c">// 分解：读取图层树，自下而上</span>`
      : `pixels, _ := editor.<span class="tok-f">Composite</span>(ctx) <span class="tok-c">// 合成：按混合规则重新渲染</span>`);
  }));
  rig.querySelectorAll('[data-eye]').forEach((button) => button.addEventListener('click', () => {
    const name = button.dataset.eye;
    const visible = state.hidden.has(name);
    if (visible) state.hidden.delete(name); else state.hidden.add(name);
    state.selected = name;
    render();
    say(`editor.<span class="tok-f">SetVisible</span>(<span class="tok-n">${ids[name]}</span>, <span class="tok-k">${visible}</span>) <span class="tok-c">// ${names[name]}</span>`);
  }));
  rig.querySelectorAll('[data-pick]').forEach((button) => button.addEventListener('click', () => select(button.dataset.pick)));
  for (const [name, el] of Object.entries(layers)) el.addEventListener('click', () => { if (state.mode === 'explode') select(name); });
  slider.addEventListener('input', () => {
    if (state.selected === 'canvas') return;
    state.opacity[state.selected] = Number(slider.value);
    render();
    const byte = Math.round(Number(slider.value) * 2.55);
    say(`editor.<span class="tok-f">SetOpacity</span>(<span class="tok-n">${ids[state.selected]}</span>, <span class="tok-n">${byte}</span>) <span class="tok-c">// ${names[state.selected]} ${slider.value}%</span>`);
  });
  maskButton.addEventListener('click', () => {
    state.masked = !state.masked;
    state.selected = 'subject';
    render();
    say(`<span class="tok-c">// 像素蒙版${state.masked ? '启用' : '停用'}（示意）。蒙版随图层保存，Composite 在支持范围内应用</span>`);
  });

  // 指针视差：轻微透视，减少动态效果时关闭。
  let target = { x: 0, y: 0 }, current = { x: 0, y: 0 }, frame = 0;
  const tilt = rig.querySelector('[data-rig-tilt]');
  const animate = () => {
    current.x += (target.x - current.x) * 0.08;
    current.y += (target.y - current.y) * 0.08;
    rig.style.setProperty('--tx', `${current.x.toFixed(2)}deg`);
    rig.style.setProperty('--ty', `${current.y.toFixed(2)}deg`);
    frame = Math.abs(target.x - current.x) + Math.abs(target.y - current.y) > 0.02 ? requestAnimationFrame(animate) : 0;
  };
  const hero = document.querySelector('.hero');
  hero?.addEventListener('pointermove', (event) => {
    if (reduced() || event.pointerType === 'touch') return;
    const box = tilt.getBoundingClientRect();
    const dx = (event.clientX - (box.left + box.width / 2)) / innerWidth;
    const dy = (event.clientY - (box.top + box.height / 2)) / innerHeight;
    target = { x: Math.max(-1, Math.min(1, dy)) * -7, y: Math.max(-1, Math.min(1, dx)) * 9 };
    if (!frame) frame = requestAnimationFrame(animate);
  });
  hero?.addEventListener('pointerleave', () => { target = { x: 0, y: 0 }; if (!frame) frame = requestAnimationFrame(animate); });
  render();
}

/* ───────── 滚动叙事 ───────── */
const story = document.querySelector('[data-story]');
if (story) {
  const board = story.querySelector('[data-story-board]');
  const steps = [...story.querySelectorAll('[data-story-steps] li')];
  const rows = [...story.querySelectorAll('[data-story-layer]')];
  const count = story.querySelector('[data-story-count]');
  const caption = story.querySelector('[data-story-caption]');
  const order = ['canvas', 'bg', 'subject', 'glow', 'ridge', 'title'];
  const stepOf = { canvas: 0, bg: 1, subject: 2, glow: 3, ridge: 4, title: 4 };
  const art = Object.fromEntries(order.map((name) => [name, board.querySelector(`[data-layer="${name}"]`)]));
  let last = -1, ticking = false;

  function update() {
    ticking = false;
    const box = story.getBoundingClientRect();
    const span = Math.max(1, box.height - innerHeight);
    const progress = Math.max(0, Math.min(1, -box.top / span));
    const step = Math.min(5, Math.floor(progress * 6.2));
    if (step === last) return;
    last = step;
    board.dataset.step = String(step);
    for (const name of order) {
      const visible = stepOf[name] <= step;
      art[name].classList.toggle('is-in', visible);
      art[name].classList.toggle('is-entering', visible && stepOf[name] === step && step < 5);
    }
    steps.forEach((item, index) => {
      item.classList.toggle('is-active', index === step);
      item.classList.toggle('is-done', index < step);
      if (index === step) item.setAttribute('aria-current', 'step'); else item.removeAttribute('aria-current');
    });
    let on = 0;
    rows.forEach((row) => {
      const level = Number(row.dataset.storyLayer);
      row.classList.toggle('is-on', level <= Math.min(step, 4));
      row.classList.toggle('is-current', level === step);
      if (level <= Math.min(step, 4)) on++;
    });
    count.textContent = String(on);
    caption.textContent = steps[step]?.querySelector('strong')?.textContent || '';
    caption.dataset.num = step === 5 ? '✓' : String(step + 1);
  }
  const request = () => { if (!ticking) { ticking = true; requestAnimationFrame(update); } };
  addEventListener('scroll', request, { passive: true });
  addEventListener('resize', request);
  update();
}

/* ───────── 入场与流程动画 ───────── */
const reveal = new IntersectionObserver((records) => {
  for (const record of records) if (record.isIntersecting) { record.target.classList.add('is-visible'); reveal.unobserve(record.target); }
}, { rootMargin: '0px 0px -12% 0px' });
document.querySelectorAll('.flow__step, .stat, .heat, .limits, .cta-card, .code-panel, .preview-panel').forEach((element, index) => {
  element.classList.add('reveal');
  element.style.transitionDelay = `${(index % 4) * 70}ms`;
  reveal.observe(element);
});
const pipeline = document.querySelector('.pipeline');
if (pipeline) new IntersectionObserver(([record]) => pipeline.classList.toggle('is-visible', record.isIntersecting)).observe(pipeline);

/* ───────── 真实 Go Wasm 预览（点击后才加载） ───────── */
const live = document.querySelector('[data-live]');
if (live) {
  const $ = (selector) => live.querySelector(selector);
  const gate = $('[data-live-gate]');
  const status = $('[data-live-status]');
  const startButton = $('[data-live-start]');
  const image = $('[data-live-image]');
  const sourceTag = $('[data-live-source]');
  const meta = $('[data-live-meta]');
  const controls = $('[data-live-controls]');
  const list = $('[data-live-layers]');
  const opacity = $('[data-live-opacity]');
  const opacityOut = $('[data-live-opacity-out]');
  const busyChip = $('[data-live-busy]');
  const busyText = $('[data-live-busy-text]');
  const codeJS = $('[data-code-js]');
  const codeGo = $('[data-code-go]');
  const downloads = { png: $('[data-live-download="png"]'), psd: $('[data-live-download="psd"]') };
  const baseJS = codeJS.innerHTML;
  const baseGo = codeGo.innerHTML;
  const printControls = $('[data-live-prints]');
  const printStatus = $('[data-live-print-status]');
  const fileInput = $('[data-live-file]');
  let selectedPrint = 'mountain', localPrint = null;
  let apparelSample = null;
  let printSessions = null;
  let sdk = null, client = null, doc = null, layers = [], selected = null, view = 'live', lastImage = null, imageURL = null, busy = false, generation = 0, replaced = false;

  const esc = (text) => String(text).replace(/[&<>"]/g, (char) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[char]);
  const str = (text) => `<span class="tok-s">'${esc(text)}'</span>`;
  const num = (value) => `<span class="tok-n">${value}</span>`;
  const jsCall = (op, rest = '') => `<span class="tok-k">await</span> engine.<span class="tok-f">call</span>(${str(op)}${rest});`;

  function appendCode(js, go, kind = '') {
    for (const [element, html] of [[codeJS, js], [codeGo, go]]) {
      if (!html) continue;
      const line = document.createElement('span');
      line.className = `code-line code-flash ${kind}`;
      line.innerHTML = html;
      element.append(line);
      const lines = element.querySelectorAll('.code-line');
      if (lines.length > 16) lines[0].remove();
      element.closest('.code-panel__body').scrollTop = 1e6;
    }
  }

  function setBusy(text) {
    busy = Boolean(text);
    busyChip.hidden = !busy;
    if (text) busyText.textContent = text;
    controls.querySelectorAll('button, input, select').forEach((element) => { element.disabled = busy; });
    printControls.querySelectorAll('button').forEach((element) => { element.disabled = busy; });
    startButton.disabled = busy || Boolean(doc);
    downloads.png.disabled = busy || !lastImage;
    downloads.psd.disabled = busy || !doc;
  }

  function tag(kind, text) {
    sourceTag.className = `source-tag source-tag--${kind}`;
    sourceTag.textContent = text;
  }

  function findImage(reply) {
    let found = null;
    const visit = (value) => {
      if (found || !value || typeof value !== 'object') return;
      if (value.image?.binary) { found = value; return; }
      Object.values(value).forEach(visit);
    };
    visit(reply?.data);
    return found && { info: found, output: reply.binaries?.find((item) => item.id === found.image.binary) };
  }

  function show(reply, kind, elapsed) {
    const result = findImage(reply);
    if (!result?.output) throw new Error('引擎没有返回图像');
    if (imageURL) URL.revokeObjectURL(imageURL);
    imageURL = URL.createObjectURL(new Blob([result.output.data], { type: result.output.mime }));
    image.src = imageURL;
    image.hidden = false;
    image.alt = kind === 'saved' ? '示例文档的保存预览（PSD 内保存的合成图）' : '示例文档的实时重绘结果（Go Wasm 输出）';
    lastImage = result.output;
    view = kind;
    tag(kind, kind === 'saved' ? '保存预览 · PSD 内保存的合成图' : '实时重绘 · Go Wasm 输出');
    meta.textContent = result.info.width + ' × ' + result.info.height + ' · ' + Math.round(elapsed) + ' ms';
    live.querySelectorAll('[data-live-view]').forEach((button) => button.setAttribute('aria-checked', String(button.dataset.liveView === view)));
  }

  async function releaseImage(reply) {
    for (const result of reply.data?.results || []) {
      if (result.handle && result.value?.image?.binary) {
        await client.call('resource.release', { target: result.handle });
      }
    }
  }

  function flatten(nodes, depth = 0) {
    return [...(nodes || [])].reverse().flatMap((layer) => [{ ...layer, depth }, ...flatten(layer.Children, depth + 1)]);
  }

  function renderLayers() {
    list.innerHTML = '';
    for (const layer of layers) {
      const item = document.createElement('li');
      item.className = `${layer.ID === selected ? 'is-selected' : ''} ${layer.Group ? 'is-group' : ''}`;
      item.style.marginLeft = `${layer.depth * 10}px`;
      item.innerHTML = `<button type="button" class="live-eye" aria-pressed="${layer.Visible}" aria-label="${layer.Visible ? '隐藏' : '显示'} ${esc(layer.Name)}"><svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7S2 12 2 12Zm10-3a3 3 0 1 0 0 6 3 3 0 0 0 0-6"/></svg></button><button type="button" class="live-pick" aria-pressed="${layer.ID === selected}">${esc(layer.Name)}${layer.Group ? ' · 组' : ''}</button>`;
      item.querySelector('.live-eye').addEventListener('click', () => toggle(layer));
      item.querySelector('.live-pick').addEventListener('click', () => { selected = layer.ID; renderLayers(); });
      list.append(item);
    }
    const current = layers.find((layer) => layer.ID === selected);
    opacity.value = String(current?.Opacity ?? 255);
    opacityOut.textContent = `${Math.round((current?.Opacity ?? 255) / 2.55)}%`;
    setBusy(busy ? busyText.textContent : '');
  }

  async function task(label, work) {
    if (busy || !client) return;
    const mine = generation;
    setBusy(label);
    try { await work(); }
    catch (error) {
      if (mine !== generation) return;
      tag('error', error.code ? `失败 · ${error.code}` : '失败');
      printMessage(`操作失败：${error.message}`, true);
      appendCode(`<span class="tok-c">// ✕ ${esc(error.code || 'error')}：${esc(error.message)}</span>`, `<span class="tok-c">// ✕ ${esc(error.message)}</span>`, 'code-line--err');
      if (!client?.worker) resetGate(`引擎已停止：${error.message}`, true);
    } finally { if (mine === generation) setBusy(''); }
  }

  async function renderLive() {
    const started = performance.now();
    const { reply, cached } = await printSessions.render();
    show(reply, 'live', performance.now() - started);
    if (cached) meta.textContent = `${reply.data.results[0].value.width} × ${reply.data.results[0].value.height} · 缓存命中`;
    appendCode(cached ? '<span class="tok-c">// ✓ 复用未变化的渲染结果</span>' : jsCall('editor.Composite', `, { target: editor, args: [{}], imageOutput: 'png' }`),
      cached ? '' : `pixels, err := editor.<span class="tok-f">Composite</span>(ctx)`);
  }

  async function renderSaved() {
    const started = performance.now();
    const reply = await client.call('preview.saved', { args: [{ $ref: doc.document }, {}], imageOutput: 'png' });
    try { show(reply, 'saved', performance.now() - started); }
    finally { await releaseImage(reply); }
    appendCode(jsCall('preview.saved', ', { args: [{ $ref: source }, {}] }'), 'saved, err := psdkit.DecodeDisplayComposite(ctx, source)');
  }

  async function refreshTree() {
    const reply = await client.call('editor.Tree', { target: doc.editor });
    const first = reply.data?.results?.[0];
    try { layers = flatten(first && Object.hasOwn(first, 'value') ? first.value : first); }
    finally { if (first?.handle) await client.call('resource.release', { target: first.handle }); }
    renderLayers();
  }

  function toggle(layer) {
    task(`${layer.Visible ? '隐藏' : '显示'} ${layer.Name}…`, async () => {
      selected = layer.ID;
      await printSessions.change('editor.SetVisible', layer.ID, !layer.Visible);
      appendCode(jsCall('editor.SetVisible', `, { target: editor, args: [${num(layer.ID)}, <span class="tok-k">${!layer.Visible}</span>] }`),
        `editor.<span class="tok-f">SetVisible</span>(${num(layer.ID)}, <span class="tok-k">${!layer.Visible}</span>) <span class="tok-c">// ${esc(layer.Name)}</span>`);
      await refreshTree();
      await renderLive();
    });
  }

  function printMessage(message, error = false) {
    printStatus.textContent = message;
    printStatus.classList.toggle('is-error', error);
  }

  function highlightPrint() {
    printControls.querySelectorAll('[data-live-print]').forEach((button) => {
      button.setAttribute('aria-pressed', String(button.dataset.livePrint === selectedPrint));
    });
  }

  async function applySelectedPrint() {
    const print = selectedPrint === 'local' ? localPrint : apparelSample.prints.find((item) => item.id === selectedPrint);
    if (!print) throw new Error('请选择一张印花');
    printMessage(`正在应用${print.name}…`);
    const cached = printSessions.entries.has(print);
    doc = await printSessions.activate(print, apparelSample.manifest.policy);
    replaced = true;
    if (!cached) {
      if (!print.contentsAsset) appendCode(jsCall('raster.buildPSD', ', { args: [{ $asset: png }, {}] }'), 'contents, err := psdkit.BuildRasterContents(ctx, pixels)');
      appendCode(jsCall('editor.ReplaceContentsWithOptions', ', { target: editor, args: [' + doc.smart + ', { $asset: psd }, ' + esc(JSON.stringify(apparelSample.manifest.policy)) + '] }'),
        'err = editor.ReplaceContentsWithOptions(ctx, ' + doc.smart + ', contents, psdkit.SmartContentsOptions{ApproximateCustomWarp: true, AssumeUntaggedSRGB: true})');
    }
    await refreshTree();
    await renderLive();
    printMessage('已应用' + print.name);
  }

  async function applyOrStart() {
    if (!doc) { await startEngine(); return; }
    await task('替换印花并重新渲染…', applySelectedPrint);
  }

  printControls.querySelectorAll('[data-live-print]').forEach((button) => button.addEventListener('click', () => {
    if (busy) return;
    selectedPrint = button.dataset.livePrint;
    highlightPrint();
    applyOrStart();
  }));
  $('[data-live-upload]').addEventListener('click', () => fileInput.click());
  fileInput.addEventListener('change', async () => {
    const file = fileInput.files?.[0];
    fileInput.value = '';
    if (!file || busy) return;
    const mine = generation;
    let ready = false;
    setBusy('准备本地印花…');
    printMessage('正在读取本地印花…');
    try {
      const dimensions = apparelSample?.manifest.document;
      const blob = await normalizePrint(file, dimensions ? { width: dimensions.contentWidth, height: dimensions.contentHeight } : {});
      if (mine !== generation) return;
      const url = URL.createObjectURL(blob);
      if (localPrint?.url) URL.revokeObjectURL(localPrint.url);
      localPrint = { id: 'local', name: file.name, blob, url };
      $('[data-live-local]').hidden = false;
      $('[data-live-local]').title = file.name;
      $('[data-live-local-image]').src = url;
      $('[data-live-local-name]').textContent = file.name;
      selectedPrint = 'local';
      highlightPrint();
      ready = true;
    } catch (error) { if (mine === generation) printMessage(error.message, true); }
    finally { if (mine === generation) setBusy(''); }
    if (ready) await applyOrStart();
  });

  opacity.addEventListener('input', () => { opacityOut.textContent = `${Math.round(Number(opacity.value) / 2.55)}%`; });
  opacity.addEventListener('change', () => {
    const layer = layers.find((item) => item.ID === selected);
    if (!layer) return;
    const value = Number(opacity.value);
    task(`调整 ${layer.Name} 不透明度…`, async () => {
      await printSessions.change('editor.SetOpacity', layer.ID, value);
      appendCode(jsCall('editor.SetOpacity', `, { target: editor, args: [${num(layer.ID)}, ${num(value)}] }`),
        `editor.<span class="tok-f">SetOpacity</span>(${num(layer.ID)}, ${num(value)}) <span class="tok-c">// ${esc(layer.Name)}</span>`);
      await refreshTree();
      await renderLive();
    });
  });
  live.querySelectorAll('[data-live-view]').forEach((button) => button.addEventListener('click', () => {
    const next = button.dataset.liveView;
    if (next === view && imageURL) return;
    task(next === 'saved' ? '读取保存预览…' : '实时重绘…', () => next === 'saved' ? renderSaved() : renderLive());
  }));
  downloads.png.addEventListener('click', () => { if (lastImage) sdk.download(lastImage, view === 'saved' ? 'muban-saved.png' : 'muban-garment.png'); });

  downloads.psd.addEventListener('click', () => task('写出 PSD…', async () => {
    const reply = await client.call('editor.WritePSD', { target: doc.editor, args: [{}] });
    const file = reply.binaries?.find((item) => item.mime === 'image/vnd.adobe.photoshop') || reply.binaries?.[0];
    if (!file) throw new Error('引擎没有返回 PSD');
    sdk.download(file, replaced ? 'muban-printed.psd' : 'muban-edited.psd');
    appendCode(jsCall('editor.WritePSD', `, { target: editor, args: [{}] }`), `err = editor.<span class="tok-f">SavePSD</span>(ctx, <span class="tok-s">"edited.psd"</span>)`);
  }));
  $('[data-live-cancel]').addEventListener('click', () => {
    client?.terminate(Object.assign(new Error('已取消：Worker 已终止，工作区已释放'), { code: 'canceled' }));
    resetGate('已取消：Worker 已终止，示例工作区已释放。', false);
  });

  function resetGate(message, isError) {
    generation++;
    client = null; doc = null; printSessions = null; layers = []; lastImage = null;
    busy = false; busyChip.hidden = true;
    printControls.hidden = true;
    controls.hidden = true;
    image.hidden = true;
    if (imageURL) URL.revokeObjectURL(imageURL);
    imageURL = null;
    gate.hidden = false;
    startButton.disabled = false;
    startButton.lastChild.textContent = '重新启动 Go 引擎';
    status.textContent = message;
    status.classList.toggle('is-error', isError);
    tag(isError ? 'error' : 'idle', isError ? '引擎错误' : '引擎未启动');
    meta.textContent = '';
    downloads.png.disabled = downloads.psd.disabled = true;
    printMessage(message, isError);
    setBusy('');
  }

  async function startEngine() {
    if (busy) return;
    const mine = ++generation;
    setBusy('加载 Go 引擎…');
    status.classList.remove('is-error');
    codeJS.innerHTML = baseJS;
    codeGo.innerHTML = baseGo;
    replaced = false;
    view = 'live';
    tag('loading', '正在加载引擎');
    status.textContent = '正在加载 SDK…';
    try {
      sdk ??= await import(`${base}demo/sdk/client.js`);
    } catch {
      resetGate('没有找到 Playground 构建产物（demo/sdk/client.js）。本地预览请先运行 make site。', true);
      return;
    }
    try {
      client = new sdk.PsdkitClient({ workerURL: `${base}demo/wasm/worker.js` });
      status.textContent = '正在下载并编译 Go WebAssembly…';
      let started = performance.now();
      const ready = await client.ready();
      if (mine !== generation) return;
      appendCode('<span class="tok-c">// ✓ 引擎就绪 ' + esc(ready.version || '') + ' · ' + Math.round(performance.now() - started) + ' ms</span>', '');
      status.textContent = '正在打开模板PSD和两张内置印花…';
      started = performance.now();
      const samples = await import(`${base}demo/sdk/samples.js`);
      apparelSample = await samples.loadGarmentSample(client, `${base}demo/samples/`);
      const created = apparelSample.opened;
      if (mine !== generation) return;
      doc = created.data;
      try {
        const objects = await client.call('editor.SmartObjects', { target: doc.editor, args: [] });
        const items = objects.data?.results?.[0]?.value;
        const preferred = apparelSample.manifest.document.preferredLayerID;
        doc.smart = items?.find((item) => item.ID === preferred)?.ID ?? items?.[0]?.ID ?? null;
        const handle = objects.data?.results?.[0]?.handle;
        if (handle) await client.call('resource.release', { target: handle });
      } catch { doc.smart = null; }
      if (!doc.smart) throw new Error('模板没有可替换的智能图层');
      printSessions = new PrintSessions(client, { asset: apparelSample.asset, document: doc });
      appendCode('<span class="tok-k">const</span> { editor, document: source, layers } = doc.data; <span class="tok-c">// ' + doc.header.Width + '×' + doc.header.Height + '，' + Math.round(performance.now() - started) + ' ms</span>', '');
      layers = flatten(doc.layers);
      selected = doc.smart ?? layers[0]?.ID;
      gate.hidden = true;
      controls.hidden = false;
      renderLayers();
      setBusy('实时重绘…');
      await applySelectedPrint();
      printControls.hidden = false;
      setBusy('');
    } catch (error) {
      if (mine !== generation) return;
      client?.close();
      resetGate('启动失败：' + error.message + (error.code ? '（' + error.code + '）' : ''), true);
    }
  }
  startButton.addEventListener('click', startEngine);

  addEventListener('pagehide', () => {
    client?.close();
    if (imageURL) URL.revokeObjectURL(imageURL);
    if (localPrint?.url) URL.revokeObjectURL(localPrint.url);
  });
}
