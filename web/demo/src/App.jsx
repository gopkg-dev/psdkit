import React, { useEffect, useMemo, useRef, useState } from 'react';
import { PsdkitClient, binary, download } from '../../sdk/client.js';
import { loadGarmentSample } from '../../sdk/samples.js';
import { Icon, Logo } from './icons.jsx';
import {
  assetKind, colorModes, defaultArgs, explain, findLayer, flattenLayers, formatBytes, imageResults, labels,
  readOnlyEditorOps, unwrap,
} from './model.js';
import { Dialog, Elapsed, Menu, Meter, Tabs } from './ui.jsx';
import { Verification } from './Verification.jsx';

// 依赖 __fixtures 的内部回归面板只在显式构建时启用，公开站点不包含。
const localVerification = import.meta.env.VITE_PSDKIT_VERIFICATION === '1';
const zoomSteps = [0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4];
const sourceNames = { saved: '保存预览', live: '实时重绘', output: '操作输出' };
const sourceHints = {
  saved: 'PSD 文件内保存的合成图，读取快，但可能是旧缓存或占位图，不反映之后的编辑。',
  live: 'Go 根据当前图层树重新合成（editor.Composite），只在支持范围内输出。',
  output: '最近一次操作返回的图像结果。',
};

export function App() {
  const client = useRef(null);
  const apparelSample = useRef(null);
  const regression = useRef(null);
  const fileInput = useRef(null);
  const [accept, setAccept] = useState('.psd,.psb,.png,.jpg,.jpeg,.ttf,.otf,.ttc,.icc,.icm');
  const [status, setStatus] = useState('loading');
  const [stage, setStage] = useState('正在下载并编译 Go WebAssembly…');
  const [version, setVersion] = useState('');
  const [catalog, setCatalog] = useState([]);
  const [doc, setDoc] = useState(null);
  const [assets, setAssets] = useState([]);
  const [resources, setResources] = useState([]);
  const [usage, setUsage] = useState(null);
  const [fonts, setFonts] = useState([]);
  const [smart, setSmart] = useState([]);
  const [selectedLayer, setSelectedLayer] = useState(null);
  const [operationID, setOperationID] = useState('preview.saved');
  const [group, setGroup] = useState('文档与源精度');
  const [argumentsText, setArgumentsText] = useState('[]');
  const [target, setTarget] = useState('');
  const [linksText, setLinksText] = useState('{}');
  const [budget, setBudget] = useState(256);
  const [preview, setPreview] = useState(null);
  const [result, setResult] = useState(null);
  const [logs, setLogs] = useState([]);
  const [error, setError] = useState(null);
  const [lastRun, setLastRun] = useState(null);
  const [busy, setBusyState] = useState(null);
  const [verificationBusy, setVerificationBusy] = useState(false);
  const [inspector, setInspector] = useState('layer');
  const [dock, setDock] = useState('log');
  const [dockOpen, setDockOpen] = useState(true);
  const [assetFilter, setAssetFilter] = useState('all');
  const [zoom, setZoom] = useState('fit');
  const [mobile, setMobile] = useState('canvas');
  const [search, setSearch] = useState('');
  const [quickSearch, setQuickSearch] = useState('');
  const [dialog, setDialog] = useState(null);
  const [dragging, setDragging] = useState(false);
  const previewURL = useRef(null);
  const isBusy = Boolean(busy) || verificationBusy;
  const ready = status === 'ready';
  const operation = catalog.find((item) => item.id === operationID);
  const layers = useMemo(() => flattenLayers(doc?.layers), [doc?.layers]);
  const activeLayer = layers.find((layer) => layer.ID === selectedLayer) || layers[0];
  const groups = [...new Set(catalog.map((item) => item.group))];
  const filteredOperations = catalog.filter((item) => item.group === group);
  const op = (id) => catalog.find((item) => item.id === id);

  function log(label, state, detail, elapsed) {
    setLogs((current) => [{ id: `${Date.now()}-${Math.random()}`, time: new Date(), label, state, detail, elapsed }, ...current].slice(0, 200));
  }

  function setBusy(label) { setBusyState(label ? { label, since: performance.now() } : null); }

  async function refreshState() {
    const state = await client.current.call('system.state');
    setAssets(state.data.assets);
    setResources(state.data.resources);
    setUsage({ assetBytes: state.data.assetBytes, retained: state.data.retainedPixelAndBinaryBytes, limits: state.data.limits });
    setFonts((current) => current.filter((face) => state.data.resources.some((item) => item.id === face.id)));
    setDoc((current) => current && (!state.data.resources.some((item) => item.id === current.document)
      || !state.data.resources.some((item) => item.id === current.editor)) ? null : current);
  }

  function clearPreview() {
    if (previewURL.current) URL.revokeObjectURL(previewURL.current);
    previewURL.current = null;
    setPreview(null);
  }

  function acceptPreview(reply, source = 'output', label = sourceNames[source]) {
    const image = imageResults(reply)[0];
    if (!image) return false;
    const output = binary(reply, image.image.binary);
    if (previewURL.current) URL.revokeObjectURL(previewURL.current);
    const url = URL.createObjectURL(new Blob([output.data], { type: output.mime }));
    previewURL.current = url;
    setPreview({ url, image, output, stale: false, source, label });
    return true;
  }

  async function acceptDocument(reply) {
    const data = reply.data;
    clearPreview();
    setDoc({ ...data, dirty: false });
    setSelectedLayer(flattenLayers(data.layers)[0]?.ID || null);
    await loadSmartObjects(data.editor);
    try {
      const saved = await client.current.call('preview.saved', { args: [{ $ref: data.document }, {}] });
      acceptPreview(saved, 'saved');
      setResult(saved);
    } catch (problem) {
      setResult(reply);
      log('保存预览不可用', 'error', problem.message);
    }
    await refreshState();
    log('读取文档', 'success', `${data.header.Width} × ${data.header.Height} · ${flattenLayers(data.layers).length} 个图层`);
    return data;
  }

  // 智能对象列表决定属性面板是否提供“替换内容”。不支持的文档返回空列表。
  async function loadSmartObjects(editor) {
    try {
      const reply = await client.current.call('editor.SmartObjects', { target: editor, args: [] });
      const list = unwrap(reply);
      setSmart(Array.isArray(list) ? list : []);
    } catch (problem) {
      setSmart([]);
      log('读取智能对象', 'error', problem.message);
    }
  }

  // 共享同一份上衣模板和两张经过校验的内置印花。
  async function openSample() {
    const started = performance.now();
    const sample = await loadGarmentSample(client.current, './samples/');
    apparelSample.current = sample;
    await acceptDocument(sample.opened);
    setSelectedLayer(sample.manifest.document.preferredLayerID);
    log('载入上衣印花样机', 'success', 'muban.psd · 山野日落 / 向阳花园 · 5 个共享衣片', performance.now() - started);
  }

  async function reopenSample() {
    setBusy('载入示例'); setError(null);
    try { await openSample(); }
    catch (problem) { setError(problem); log('载入示例', 'error', problem.message); }
    finally { setBusy(null); }
  }

  async function replaceSmartContents(layer, assetID) {
    const asset = assets.find((item) => item.id === assetID);
    if (!asset || !doc) return;
    let contents = assetID;
    if (asset.kind === 'image') {
      setBusy('图片转为可编辑 PSD'); setError(null);
      const started = performance.now();
      try {
        const built = await client.current.call('raster.buildPSD', { args: [{ $asset: assetID }, {}] });
        const file = built.binaries?.find((item) => item.mime !== 'application/x-rgba8');
        if (!file) throw new Error('raster.buildPSD 没有返回 PSD 数据');
        const put = await client.current.call('asset.put', { bytes: file.data, name: `${asset.name.replace(/\.[^.]+$/, '')}.psd`, kind: 'contents' });
        contents = put.data.asset;
        log('图片转可编辑 PSD', 'success', `${asset.name} → ${formatBytes(file.length)}`, performance.now() - started);
      } catch (problem) {
        setError(problem); log('图片转可编辑 PSD', 'error', problem.message);
        setBusy(null);
        return;
      }
    }
    const sample = apparelSample.current?.asset === doc.asset ? apparelSample.current : null;
    const method = sample ? 'editor.ReplaceContentsWithOptions' : 'editor.ReplaceContents';
    const args = [layer.ID, { $asset: contents }, ...(sample ? [sample.manifest.policy] : [])];
    await run(op(method), args, doc.editor, '替换智能对象印花');
  }

  async function boot() {
    setStatus('loading'); setError(null); setBusy('启动引擎');
    try {
      setStage('正在下载并编译 Go WebAssembly…');
      const started = performance.now();
      const readiness = await client.current.ready();
      setVersion(readiness.version || '');
      setStage('正在读取能力目录…');
      const capabilities = await client.current.call('system.capabilities');
      setCatalog(capabilities.data);
      setStatus('ready');
      log('引擎就绪', 'success', `${capabilities.data.length} 个 Go 入口`, performance.now() - started);
      setStage('正在载入上衣模板和两张印花…');
      await openSample();
    } catch (problem) {
      setError(problem); setStatus(client.current.worker ? 'failed' : 'stopped');
      log('启动引擎', 'error', problem.message);
    } finally { setBusy(null); }
  }

  function startEngine() {
    client.current?.close();
    client.current = new PsdkitClient();
    setCatalog([]); setDoc(null); setAssets([]); setResources([]); setFonts([]); setUsage(null); setResult(null); clearPreview();
    boot();
  }

  useEffect(() => {
    startEngine();
    return () => { client.current?.close(); if (previewURL.current) URL.revokeObjectURL(previewURL.current); };
  }, []);

  // 有未导出的编辑时，离开页面前提示。
  useEffect(() => {
    if (!doc?.dirty) return undefined;
    const guard = (event) => { event.preventDefault(); event.returnValue = ''; };
    addEventListener('beforeunload', guard);
    return () => removeEventListener('beforeunload', guard);
  }, [doc?.dirty]);

  useEffect(() => {
    if (!operation) return;
    setArgumentsText(JSON.stringify(defaultArgs(operation, doc, activeLayer, assets, fonts), null, 2));
    const selected = operation.targetKind === 'asset' ? assets[0] : resources.find((item) => operation.targetKind === 'resource' || item.kind === operation.targetKind);
    setTarget(operation.targetKind === 'editor' ? doc?.editor || ''
      : operation.targetKind === 'document' ? doc?.document || '' : selected?.id || '');
  }, [operationID, doc?.document, selectedLayer, fonts.length, assets.length]);

  async function importFiles(files) {
    if (!files.length) return;
    if (!ready || busy) {
      setError(Object.assign(new Error(ready ? '当前有操作正在执行，请稍候再导入。' : 'Go 引擎尚未就绪，请稍候再导入。'), { code: 'invalid_request' }));
      return;
    }
    setBusy(`导入 ${files.length} 个文件`); setError(null);
    try {
      for (const file of files) {
        const kind = assetKind(file.name);
        const started = performance.now();
        const reply = await client.current.importFile(file, kind);
        const id = reply.data.asset;
        if (kind === 'document') await acceptDocument(await client.current.call('document.open', { args: [{ $asset: id }] }));
        if (kind === 'font') {
          const loaded = await client.current.call('font.decode', { args: [{ $asset: id }, {}] });
          const face = loaded.data.results[0];
          setFonts((current) => [...current, { id: face.handle, name: face.value.PostScriptName, asset: id }]);
          setResult(loaded);
        }
        log(`导入 ${file.name}`, 'success', `${formatBytes(file.size)} · ${kind}`, performance.now() - started);
      }
      await refreshState();
    } catch (problem) {
      setError(problem); log('导入文件', 'error', problem.message);
      if (!client.current.worker) setStatus('stopped');
      else await refreshState().catch(() => {});
    } finally { setBusy(null); }
  }

  function openPicker(filter) {
    setAccept(filter || '.psd,.psb,.png,.jpg,.jpeg,.ttf,.otf,.ttc,.icc,.icm');
    setTimeout(() => fileInput.current?.click(), 0);
  }

  async function refreshEditor() {
    if (!doc) return;
    const tree = await client.current.call('editor.Tree', { target: doc.editor });
    setDoc((current) => ({ ...current, layers: unwrap(tree), dirty: true }));
    if (smart.length) await loadSmartObjects(doc.editor);
  }

  async function run(chosen = operation, argsOverride, targetOverride, busyLabel) {
    if (!chosen || !client.current) return null;
    if (chosen.id === 'asset.put') { openPicker(); return null; }
    const label = labels[chosen.label] || chosen.label;
    setBusy(busyLabel || label); setError(null);
    setLastRun({ chosen, argsOverride, targetOverride, busyLabel });
    const started = performance.now();
    try {
      const args = argsOverride || JSON.parse(argumentsText);
      if (!Array.isArray(args)) throw Object.assign(new Error('高级参数必须是与 Go 签名对应的 JSON 数组'), { code: 'invalid_request' });
      const bounded = args.map((argument, index) => {
        const schema = chosen.parameters[index]?.schema?.properties || {};
        if (!argument || typeof argument !== 'object' || Array.isArray(argument) || argument.$ref || argument.$asset) return argument;
        const owned = { ...argument };
        for (const field of ['MaxBytes', 'MaxWorkingBytes', 'MaxOutputBytes']) if (Object.hasOwn(schema, field)) owned[field] = budget * 1024 * 1024;
        return owned;
      });
      const reply = await client.current.call(chosen.id, {
        target: targetOverride ?? target, args: bounded, links: JSON.parse(linksText),
      });
      setResult(reply);
      acceptPreview(reply, chosen.id.startsWith('preview.') ? 'saved' : chosen.id === 'editor.Composite' ? 'live' : 'output',
        chosen.id.startsWith('preview.') || chosen.id === 'editor.Composite' ? undefined : `操作输出 · ${label}`);
      log(label, 'success', `${reply.binaries?.length || 0} 个二进制输出`, performance.now() - started);
      if (['document.create', 'demo.create', 'document.open'].includes(chosen.id)) await acceptDocument(reply);
      else if (chosen.id.startsWith('editor.') && !readOnlyEditorOps.has(chosen.id)) {
        await refreshEditor();
        setPreview((current) => current ? { ...current, stale: true } : current);
        try {
          const rendered = await client.current.call('editor.Composite', { target: doc.editor, args: [{}] });
          acceptPreview(rendered, 'live');
        } catch (problem) {
          setError(Object.assign(new Error(`编辑已记录；实时重绘被库拒绝：${problem.message}`), { code: problem.code, path: problem.path }));
          log('实时重绘', 'error', problem.message);
        }
      }
      await refreshState();
      if (chosen.id === 'system.reset' || chosen.id === 'resource.release' && [doc?.document, doc?.editor].includes(targetOverride ?? target)) clearPreview();
      return reply;
    } catch (problem) {
      setError(problem);
      setResult({ ok: false, data: { error: { code: problem.code, message: problem.message, path: problem.path, feature: problem.feature, offset: problem.offset } } });
      log(label, 'error', problem.message, performance.now() - started);
      if (!client.current?.worker) { setStatus('stopped'); setDoc(null); setAssets([]); setResources([]); setFonts([]); clearPreview(); }
      return null;
    } finally { setBusy(null); }
  }

  const editLayer = (id, args, busyLabel) => run(op(id), args, doc.editor, busyLabel);

  async function showSaved() {
    if (!doc) return;
    await run(op('preview.saved'), [{ $ref: doc.document }, {}], '', '读取保存预览');
  }

  async function showLive() {
    if (!doc) return;
    await run(op('editor.Composite'), [{}], doc.editor, '实时重绘');
  }

  async function exportPSD() {
    const reply = await run(op('editor.WritePSD'), [{}], doc.editor, '写出 PSD');
    const file = reply?.binaries?.find((item) => item.mime === 'image/vnd.adobe.photoshop');
    if (!file) return;
    download(file, doc.name.replace(/\.(psd|psb)$/i, '') + (doc.header.Version === 2 ? '.psb' : '.psd'));
    setDoc((current) => current && { ...current, dirty: false });
  }

  async function exportLivePNG() {
    const reply = await run(op('editor.Composite'), [{}], doc.editor, '实时重绘并导出');
    const image = reply && imageResults(reply)[0];
    if (image) download(binary(reply, image.image.binary), `${doc.name.replace(/\.(psd|psb)$/i, '')}-live.png`);
  }

  function changeVisibility(layer) {
    editLayer('editor.SetVisible', [layer.ID, !layer.Visible], `${layer.Visible ? '隐藏' : '显示'} ${layer.Name}`);
  }

  function selectOperation(id) {
    const selected = op(id);
    if (!selected) return;
    setOperationID(id); setGroup(selected.group); setInspector('operations'); setMobile('operations');
  }

  function cancel() {
    regression.current?.();
    client.current?.terminate(Object.assign(new Error('已取消：Worker 已终止，工作区与未导出的编辑已释放'), { code: 'canceled' }));
    setStatus('stopped'); setDoc(null); setAssets([]); setResources([]); setFonts([]); setUsage(null);
    clearPreview(); setBusy(null); setResult(null);
    log('终止 Worker', 'error', '已释放全部会话');
  }

  function confirmThen(action, title, message) {
    if (!doc?.dirty) { action(); return; }
    setDialog({ type: 'confirm', title, message, action });
  }

  async function releaseAll() {
    await run(op('system.reset'), [], '', '释放全部资源');
    setDoc(null); clearPreview();
  }

  function updateArgument(index, value) {
    try { const args = JSON.parse(argumentsText); args[index] = value; setArgumentsText(JSON.stringify(args, null, 2)); }
    catch { setError(Object.assign(new Error('请先修正高级参数 JSON'), { code: 'invalid_request' })); }
  }

  // 快捷键：⌘/Ctrl+O 打开，⌘/Ctrl+S 导出 PSD，+ / - / 0 缩放。
  useEffect(() => {
    function onKey(event) {
      const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(event.target.tagName);
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'o') { event.preventDefault(); if (ready && !isBusy) openPicker(); }
      else if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's') { event.preventDefault(); if (doc && !isBusy) exportPSD(); }
      else if (!typing && !event.metaKey && !event.ctrlKey && preview) {
        if (event.key === '+' || event.key === '=') stepZoom(1);
        else if (event.key === '-') stepZoom(-1);
        else if (event.key === '0') setZoom('fit');
      }
    }
    addEventListener('keydown', onKey);
    return () => removeEventListener('keydown', onKey);
  });

  function stepZoom(direction) {
    const current = zoom === 'fit' ? 1 : Number(zoom);
    const index = zoomSteps.findIndex((value) => value >= current);
    const next = zoomSteps[Math.max(0, Math.min(zoomSteps.length - 1, (index === -1 ? zoomSteps.length - 1 : index) + direction))];
    setZoom(String(next));
  }

  const visibleAssets = assets.filter((asset) => assetFilter === 'all' || asset.kind === assetFilter);
  const targetOptions = operation?.targetKind === 'asset' ? assets : operation?.targetKind ? resources.filter((item) => operation.targetKind === 'resource' || item.kind === operation.targetKind) : [];
  const currentArgs = (() => { try { return JSON.parse(argumentsText); } catch { return []; } })();
  const displayedCapabilities = catalog.filter((item) => `${item.group} ${item.label} ${labels[item.label] || ''} ${item.id} ${item.goAPI} ${item.description}`.toLowerCase().includes(search.toLowerCase()));
  const quickMatches = quickSearch ? catalog.filter((item) => `${item.label} ${labels[item.label] || ''} ${item.id} ${item.goAPI}`.toLowerCase().includes(quickSearch.toLowerCase())).slice(0, 8) : [];
  const imageAssets = assets.filter((asset) => asset.kind === 'image');
  const hint = error ? explain(error) : null;
  const isDocumentError = error && !client.current?.worker;

  return <div className={`pg mobile-${mobile} engine-${status} ${dockOpen ? '' : 'dock-closed'}`}
    onDragOver={(event) => { if ([...event.dataTransfer.types].includes('Files')) { event.preventDefault(); setDragging(true); } }}
    onDragLeave={(event) => { if (event.target === event.currentTarget || !event.relatedTarget) setDragging(false); }}
    onDrop={(event) => { event.preventDefault(); setDragging(false); importFiles([...event.dataTransfer.files]); }}>
    <input ref={fileInput} className="visually-hidden" type="file" multiple accept={accept} tabIndex={-1} aria-hidden="true"
      onChange={(event) => { importFiles([...event.target.files]); event.target.value = ''; }} />

    {/* ───────── 顶栏 ───────── */}
    <header className="pg-top">
      <a className="pg-brand" href="../" aria-label="psdkit 首页"><Logo size={24} /><span>psdkit</span></a>
      <span className="pg-product">Playground</span>
      <div className="pg-menus">
        <Menu label="文件" icon="folder" disabled={!ready || isBusy} items={[
          { label: '打开本机文件…', hint: 'PSD/PSB、PNG/JPEG、字体、ICC', icon: 'upload', shortcut: '⌘O', onSelect: () => openPicker() },
          '-',
          { label: '打开上衣印花样机', hint: 'muban.psd · 两张内置印花 · 共享衣片联动', icon: 'box', onSelect: () => confirmThen(reopenSample, '切换到上衣示例？', '当前文档有未导出的编辑。打开示例不会释放它，但画布将切换到新文档。') },
          { label: '新建简单示例', hint: 'demo.create：Go 现场生成的 6 图层文档', icon: 'spark', onSelect: () => confirmThen(() => run(op('demo.create'), [], '', '新建示例'), '切换到新文档？', '当前文档有未导出的编辑。新建示例不会释放它，但画布将切换到新文档。') },
          { label: '新建空白文档…', hint: '透明 RGB8 PSD / PSB', icon: 'plus', onSelect: () => setDialog({ type: 'new', width: 640, height: 480, version: 1 }) },
        ]} />
        <Menu label="导出" icon="download" disabled={!doc || isBusy} primary items={[
          { label: '当前画面 PNG', hint: preview ? `${preview.label}${preview.stale ? '（上次结果）' : ''}` : '暂无画面', icon: 'image', disabled: !preview, onSelect: () => download(preview.output, `psdkit-${preview.source}.png`) },
          { label: '实时重绘 PNG', hint: 'editor.Composite', icon: 'layers', onSelect: exportLivePNG },
          { label: `可编辑 ${doc?.header.Version === 2 ? 'PSB' : 'PSD'}`, hint: 'editor.WritePSD，保留未修改的原始块', icon: 'file', shortcut: '⌘S', onSelect: exportPSD },
        ]} />
        <Menu label="工作区" icon="box" disabled={!client.current} items={[
          { label: '释放全部资源', hint: 'system.reset：关闭所有文档、句柄与资产', icon: 'trash', disabled: !ready || isBusy, onSelect: () => confirmThen(releaseAll, '释放全部资源？', '当前文档有未导出的编辑，释放后无法恢复。') },
          { label: '重启引擎', hint: '终止 Worker 并重新加载 Wasm', icon: 'reset', onSelect: () => confirmThen(startEngine, '重启引擎？', '重启会丢弃整个工作区，包括未导出的编辑。') },
        ]} />
      </div>
      <div className="pg-doc-title" title={doc?.name}>
        {doc ? <><Icon name="file" size={15} /><span>{doc.name}</span>{doc.dirty ? <i className="dirty-dot" aria-label="有未导出的编辑" title="有未导出的编辑" /> : null}</> : null}
      </div>
      <div className="pg-top__right">
        {busy ? <button type="button" className="tb-btn tb-btn--danger" onClick={cancel} title="终止 Worker 将释放整个工作区"><Icon name="stop" size={14} /><span className="tb-btn__label">取消</span></button> : null}
        <span className={`engine-pill engine-pill--${status}`} role="status">
          <i />{status === 'ready' ? (busy ? '处理中' : 'Go 引擎就绪') : status === 'loading' ? '加载引擎' : status === 'failed' ? '启动失败' : '已停止'}
        </span>
        <a className="tb-icon" href="../guide/" aria-label="使用文档" title="使用文档"><Icon name="book" size={18} /></a>
      </div>
    </header>

    <nav className="pg-mobile-nav" aria-label="工作区区域">
      {[['layers', '图层', 'layers'], ['canvas', '画布', 'image'], ['inspect', '属性', 'sliders'], ['operations', '操作', 'code']].map(([id, text, icon]) =>
        <button key={id} type="button" aria-pressed={mobile === id} onClick={() => { setMobile(id); if (id === 'inspect') setInspector('layer'); if (id === 'operations') setInspector('operations'); }}>
          <Icon name={icon} size={17} />{text}
        </button>)}
    </nav>

    {/* ───────── 左栏：图层与资源 ───────── */}
    <aside className="pg-left" aria-label="图层与资源">
      <section className="panel panel--layers">
        <div className="panel__head">
          <h2>图层 <span className="count">{layers.length}</span></h2>
          <div className="panel__tools">
            <button type="button" className="icon-btn" disabled={!doc || isBusy} aria-label="新建图层组" title="新建图层组"
              onClick={() => setDialog({ type: 'group', name: '新建组' })}><Icon name="folder" size={16} /></button>
            <button type="button" className="icon-btn" disabled={!doc || isBusy || !imageAssets.length} aria-label="添加图片图层" title={imageAssets.length ? '添加图片图层' : '先导入 PNG/JPEG'}
              onClick={() => setDialog({ type: 'raster', asset: imageAssets[0]?.id, name: imageAssets[0]?.name.replace(/\.[^.]+$/, '') || '图片', x: 0, y: 0 })}><Icon name="plus" size={16} /></button>
          </div>
        </div>
        <div className="layer-tree" role="tree" aria-label="图层树" onKeyDown={(event) => {
          if (!['ArrowDown', 'ArrowUp'].includes(event.key)) return;
          event.preventDefault();
          const index = layers.findIndex((layer) => layer.ID === activeLayer?.ID);
          const next = layers[Math.max(0, Math.min(layers.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))];
          if (next) { setSelectedLayer(next.ID); event.currentTarget.querySelector(`[data-layer-id="${next.ID}"] .layer-row__name`)?.focus(); }
        }}>
          {layers.map((layer) => <div key={layer.ID} role="treeitem" aria-selected={layer.ID === activeLayer?.ID} aria-level={layer.depth + 1}
            data-layer-id={layer.ID} className={`layer-row ${layer.ID === activeLayer?.ID ? 'is-selected' : ''} ${layer.Visible ? '' : 'is-hidden'}`}
            style={{ '--depth': layer.depth }}>
            <button type="button" className="layer-row__eye" aria-pressed={layer.Visible} aria-label={`${layer.Visible ? '隐藏' : '显示'} ${layer.Name}`}
              disabled={isBusy} onClick={() => changeVisibility(layer)}><Icon name={layer.Visible ? 'eye' : 'eyeOff'} size={15} /></button>
            <button type="button" className="layer-row__name" tabIndex={layer.ID === activeLayer?.ID ? 0 : -1}
              onClick={() => { setSelectedLayer(layer.ID); setInspector('layer'); }} onDoubleClick={() => { setSelectedLayer(layer.ID); setInspector('layer'); setMobile('inspect'); }}>
              <span className={`layer-row__thumb ${layer.Group ? 'is-group' : ''} ${smart.some((item) => item.ID === layer.ID) ? 'is-smart' : ''}`} title={smart.some((item) => item.ID === layer.ID) ? '智能对象' : undefined}><Icon name={layer.Group ? 'folder' : smart.some((item) => item.ID === layer.ID) ? 'box' : 'image'} size={14} /></span>
              <span className="layer-row__label">{layer.Name}</span>
              {layer.Opacity < 255 ? <span className="layer-row__op">{Math.round(layer.Opacity / 2.55)}%</span> : null}
            </button>
          </div>)}
          {!layers.length ? <p className="empty">{ready ? '打开 PSD / PSB 或新建文档' : '引擎加载后显示图层'}</p> : null}
        </div>
      </section>

      <section className="panel panel--assets">
        <div className="panel__head">
          <h2>已导入资源 <span className="count">{assets.length}</span></h2>
          <div className="panel__tools">
            <button type="button" className="icon-btn" disabled={!ready || isBusy} aria-label="导入文件" title="导入文件" onClick={() => openPicker()}><Icon name="upload" size={16} /></button>
          </div>
        </div>
        <div className="chips" role="group" aria-label="资源类型">
          {[['all', '全部'], ['document', '文档'], ['image', '图片'], ['font', '字体'], ['profile', 'ICC']].map(([id, text]) =>
            <button key={id} type="button" aria-pressed={assetFilter === id} onClick={() => setAssetFilter(id)}>{text}</button>)}
        </div>
        <ul className="asset-list">
          {visibleAssets.map((asset) => <li key={asset.id} className="asset-row">
            <span className={`asset-row__icon asset-row__icon--${asset.kind}`}><Icon name={{ font: 'font', document: 'file', profile: 'spark' }[asset.kind] || 'image'} size={15} /></span>
            <span className="asset-row__text"><strong title={asset.name}>{asset.name}</strong><small>{asset.id} · {formatBytes(asset.bytes)}</small></span>
            <button type="button" className="icon-btn icon-btn--sm" disabled={isBusy} aria-label={`删除资源 ${asset.name}`} title="删除资源（asset.remove）"
              onClick={() => run(op('asset.remove'), [], asset.id, `删除 ${asset.name}`)}><Icon name="trash" size={14} /></button>
          </li>)}
        </ul>
        {!visibleAssets.length ? <p className="empty">{assetFilter === 'font' ? '导入字体以启用真实排版；不会读取系统字体。' : '导入图片、ICC 或字体作为操作输入。'}</p> : null}
        <button type="button" className="drop-zone" disabled={!ready || isBusy} onClick={() => openPicker()}>
          <Icon name="upload" size={20} /><span>点击或拖入文件</span><small>文件只在本机内存中处理，不会上传</small>
        </button>
      </section>
    </aside>

    {/* ───────── 中央：画布与执行记录 ───────── */}
    <main className="pg-center" aria-label="画布">
      <div className="canvas-bar">
        <div className="canvas-bar__source">
          {preview ? <span className={`source-tag source-tag--${preview.source}`} title={sourceHints[preview.source]}>{preview.label}</span> : <span className="source-tag source-tag--idle">无画面</span>}
          {preview?.stale ? <span className="stale-tag"><Icon name="warning" size={13} />上次结果，尚未反映编辑</span> : null}
        </div>
        <div className="seg" role="group" aria-label="画面来源">
          <button type="button" aria-pressed={preview?.source === 'saved'} disabled={!doc || isBusy} onClick={showSaved} title={sourceHints.saved}>保存预览</button>
          <button type="button" aria-pressed={preview?.source === 'live'} disabled={!doc || isBusy} onClick={showLive} title={sourceHints.live}>实时重绘</button>
        </div>
        <div className="zoom" role="group" aria-label="缩放">
          <button type="button" className="icon-btn icon-btn--sm" aria-label="缩小" disabled={!preview} onClick={() => stepZoom(-1)}><Icon name="minus" size={14} /></button>
          <select aria-label="缩放比例" value={zoom} disabled={!preview} onChange={(event) => setZoom(event.target.value)}>
            <option value="fit">适应</option>
            {zoomSteps.map((value) => <option key={value} value={String(value)}>{value * 100}%</option>)}
          </select>
          <button type="button" className="icon-btn icon-btn--sm" aria-label="放大" disabled={!preview} onClick={() => stepZoom(1)}><Icon name="plus" size={14} /></button>
          <button type="button" className="icon-btn icon-btn--sm" aria-label="适应窗口" disabled={!preview} onClick={() => setZoom('fit')}><Icon name="fit" size={14} /></button>
        </div>
      </div>

      {error ? <div className={`notice notice--${error.code === 'unsupported' || error.code === 'missing_font' || error.code === 'missing_glyph' ? 'warn' : 'error'}`} role="alert">
        <Icon name="warning" size={18} />
        <div className="notice__body">
          <strong>{hint.title}<code>{error.code || 'error'}</code></strong>
          <p>{hint.hint}</p>
          <p className="notice__raw">{error.message}</p>
          {error.path || error.feature || error.offset ? <p className="notice__meta">{error.feature ? <span>feature: <code>{error.feature}</code></span> : null}{error.path ? <span>path: <code>{error.path}</code></span> : null}{error.offset ? <span>offset: <code>{error.offset}</code></span> : null}</p> : null}
        </div>
        <div className="notice__actions">
          {['missing_font', 'missing_glyph'].includes(error.code) && ready ? <button type="button" className="tb-btn" onClick={() => openPicker('.ttf,.otf,.ttc')}><Icon name="font" size={14} />导入字体</button> : null}
          {status === 'stopped' || status === 'failed' ? <button type="button" className="tb-btn" onClick={startEngine}><Icon name="reset" size={14} />重新启动</button>
            : lastRun && ready && !isDocumentError ? <button type="button" className="tb-btn" disabled={isBusy} onClick={() => run(lastRun.chosen, lastRun.argsOverride, lastRun.targetOverride, lastRun.busyLabel)}><Icon name="reset" size={14} />重试</button> : null}
          <button type="button" className="icon-btn icon-btn--sm" aria-label="关闭提示" onClick={() => setError(null)}><Icon name="close" size={14} /></button>
        </div>
      </div> : null}

      <div className={`canvas-area checkerboard-bg ${dragging ? 'is-dragging' : ''}`}>
        {preview ? zoom === 'fit'
          ? <img className="doc-image doc-image--fit" src={preview.url} alt={`${preview.label}：${preview.image.width} × ${preview.image.height}`} draggable="false" />
          : <div className="doc-canvas" style={{ width: preview.image.width * Number(zoom), height: preview.image.height * Number(zoom) }}>
            <img className="doc-image" src={preview.url} alt={`${preview.label}：${preview.image.width} × ${preview.image.height}`} draggable="false" />
          </div> : <div className="canvas-state">
          {status === 'loading' || (busy && status === 'ready') ? <><span className="spinner spinner--lg" /><strong>{status === 'loading' ? '正在启动 Go 引擎' : busy.label}</strong><p>{status === 'loading' || busy.label === '启动引擎' ? stage : '处理完成后显示画面'}</p>{status === 'loading' ? <small>Wasm 约 14 MB，仅首次加载；之后由浏览器缓存。</small> : null}</>
            : status === 'failed' || status === 'stopped' ? <><Icon name="cpu" size={40} /><strong>{status === 'failed' ? '引擎启动失败' : '引擎已停止'}</strong><p>{status === 'failed' ? '无法加载或初始化 Wasm。请检查网络，或确认 wasm/ 资源已随站点部署。' : 'Worker 已终止，工作区与未导出的编辑已释放。'}</p><button type="button" className="tb-btn tb-btn--primary" onClick={startEngine}><Icon name="reset" size={15} />重新启动引擎</button></>
              : <><Icon name="layers" size={40} /><strong>没有打开的文档</strong><p>拖入 PSD / PSB，或从“文件”菜单新建。</p><button type="button" className="tb-btn tb-btn--primary" disabled={isBusy} onClick={() => openPicker()}><Icon name="upload" size={15} />打开本机文件</button></>}
        </div>}
        {dragging ? <div className="drop-overlay"><Icon name="upload" size={30} /><strong>松开以导入</strong><small>PSD/PSB 打开为文档，图片 / 字体 / ICC 成为资源</small></div> : null}
        {busy && status !== 'loading' ? <div className="busy" role="status" aria-live="polite">
          <span className="spinner" /><span>{busy.label}</span><Elapsed since={busy.since} />
          <button type="button" onClick={cancel} title="终止 Worker：释放整个工作区">取消</button>
        </div> : null}
        {preview ? <div className="canvas-meta">{preview.image.width} × {preview.image.height}px{zoom !== 'fit' ? ` · ${Number(zoom) * 100}%` : ''}</div> : null}
      </div>

      <section className="dock" aria-label="执行记录与能力目录">
        <Tabs label="底部面板" value={dock} onChange={(id) => { setDock(id); setDockOpen(true); }} className="dock__tabs" tabs={[
          { id: 'log', label: '执行记录', badge: logs.length || null },
          { id: 'capabilities', label: '能力目录', badge: catalog.length || null },
          { id: 'tree', label: '图层数据' },
          ...(localVerification ? [{ id: 'verification', label: '浏览器回归' }] : []),
        ]} trailing={<div className="dock__tools">
          {dock === 'log' && logs.length ? <button type="button" className="text-btn" onClick={() => setLogs([])}><Icon name="trash" size={14} />清空</button> : null}
          <button type="button" className="icon-btn icon-btn--sm" aria-expanded={dockOpen} aria-label={dockOpen ? '收起底部面板' : '展开底部面板'} onClick={() => setDockOpen(!dockOpen)}>
            <Icon name="chevron" size={15} style={{ transform: dockOpen ? 'none' : 'rotate(180deg)' }} /></button>
        </div>} />
        <div className="dock__body" hidden={!dockOpen}>
          {dock === 'log' ? <div role="tabpanel" id="panel-log" aria-labelledby="tab-log">
            {logs.length ? <table className="log-table"><thead><tr><th>时间</th><th>操作</th><th>状态</th><th>输出</th><th>耗时</th></tr></thead><tbody>
              {logs.map((entry) => <tr key={entry.id}><td>{entry.time.toLocaleTimeString('zh-CN', { hour12: false })}</td><td>{entry.label}</td>
                <td><span className={`state state--${entry.state}`}><i />{entry.state === 'success' ? '完成' : '失败'}</span></td>
                <td title={entry.detail}>{entry.detail}</td><td>{entry.elapsed == null ? '—' : `${entry.elapsed.toFixed(0)} ms`}</td></tr>)}
            </tbody></table> : <p className="empty">执行的每次 Go 调用都会记录在这里。</p>}
          </div> : null}
          {dock === 'capabilities' ? <div role="tabpanel" id="panel-capabilities" aria-labelledby="tab-capabilities" className="catalog">
            <div className="catalog__search"><Icon name="search" size={15} /><input aria-label="搜索能力" placeholder="搜索能力、Go API 或原始说明，例如 SetOpacity、Warp" value={search} onChange={(event) => setSearch(event.target.value)} /><span>{displayedCapabilities.length} / {catalog.length}</span></div>
            <p className="muted">入口可调用不代表当前文档支持；精度门禁与结构化错误由原 Go 库决定。</p>
            <ul className="catalog__list">
              {displayedCapabilities.map((item) => <li key={item.id}><button type="button" onClick={() => selectOperation(item.id)}>
                <span className="catalog__label">{labels[item.label] || item.label}</span><code>{item.goAPI}</code><small>{item.group}</small>
                {item.policy ? <span className="catalog__policy" title={item.policy}>策略</span> : null}
              </button></li>)}
            </ul>
          </div> : null}
          {dock === 'tree' ? <div role="tabpanel" id="panel-tree" aria-labelledby="tab-tree"><pre className="json">{JSON.stringify(doc?.layers || [], null, 2)}</pre></div> : null}
          {localVerification ? <div role="tabpanel" id="panel-verification" aria-labelledby="tab-verification" hidden={dock !== 'verification'}>
            <Verification disabled={Boolean(busy)} cancelRef={regression} onBusy={setVerificationBusy} log={log} />
          </div> : null}
        </div>
      </section>
    </main>

    {/* ───────── 右栏：属性、操作、结果 ───────── */}
    <aside className="pg-right" aria-label="检查器">
      <Tabs label="检查器" value={inspector} onChange={(id) => { setInspector(id); setMobile(id === 'operations' ? 'operations' : 'inspect'); }} className="inspector-tabs" tabs={[
        { id: 'layer', label: '属性', icon: 'sliders' }, { id: 'operations', label: '操作', icon: 'code' }, { id: 'results', label: '结果', icon: 'box' },
      ]} />

      {inspector === 'layer' ? <div className="inspector" role="tabpanel" id="panel-layer" aria-labelledby="tab-layer">
        {doc && activeLayer ? <LayerProperties key={`${activeLayer.ID}-${activeLayer.Name}-${activeLayer.Opacity}-${activeLayer.FillOpacity}-${activeLayer.Visible}`}
          layer={activeLayer} busy={isBusy} imageAssets={imageAssets}
          onRename={(name) => editLayer('editor.Rename', [activeLayer.ID, name], '重命名图层')}
          onVisible={() => changeVisibility(activeLayer)}
          onOpacity={(value) => editLayer('editor.SetOpacity', [activeLayer.ID, value], '调整不透明度')}
          onFill={(value) => editLayer('editor.SetFillOpacity', [activeLayer.ID, value], '调整填充')}
          onReplace={(asset) => editLayer('editor.ReplacePixels', [activeLayer.ID, { $asset: asset }], '替换像素')}
          onRemove={() => setDialog({ type: 'confirm', tone: 'danger', title: `删除图层“${activeLayer.Name}”？`, message: '删除会记录到编辑器；导出 PSD 前可以通过重新打开原文件恢复。', action: () => editLayer('editor.Remove', [activeLayer.ID], '删除图层') })}
          onOperations={() => { setGroup('图层与编辑'); setOperationID('editor.Move'); setInspector('operations'); setMobile('operations'); }}
          onImport={() => openPicker('.png,.jpg,.jpeg')}
          smart={smart.find((item) => item.ID === activeLayer.ID)}
          apparel={apparelSample.current?.asset === doc.asset}
          contentAssets={assets.filter((asset) => ['image', 'contents'].includes(asset.kind) || asset.kind === 'document' && asset.id !== doc.asset)}
          onReplaceContents={(asset) => replaceSmartContents(activeLayer, asset)} /> : <p className="empty">{doc ? '选择一个图层查看属性' : '打开文档后在这里编辑图层'}</p>}

        {doc ? <section className="ins-section">
          <h3>文档</h3>
          <dl className="facts">
            <div><dt>尺寸</dt><dd>{doc.header.Width} × {doc.header.Height}</dd></div>
            <div><dt>格式</dt><dd>{doc.header.Version === 2 ? 'PSB' : 'PSD'}</dd></div>
            <div><dt>位深</dt><dd>{doc.header.Depth} 位 / 通道</dd></div>
            <div><dt>色彩</dt><dd>{colorModes[doc.header.ColorMode] ?? `模式 ${doc.header.ColorMode}`} · {doc.header.Channels} 通道</dd></div>
            <div><dt>图层</dt><dd>{layers.length}</dd></div>
            <div><dt>状态</dt><dd>{doc.dirty ? <span className="warn-text">有未导出的编辑</span> : '与打开时一致'}</dd></div>
          </dl>
        </section> : null}

        {usage ? <section className="ins-section">
          <h3>资源用量 <small>预算由 Go 执行</small></h3>
          <Meter label="资产" used={usage.assetBytes} limit={usage.limits.maxBytes} format={formatBytes} />
          <Meter label="保留像素与二进制" used={usage.retained} limit={usage.limits.maxBytes} format={formatBytes} />
          <Meter label="资产数量" used={assets.length} limit={usage.limits.maxAssets} />
          <Meter label="句柄数量" used={resources.length} limit={usage.limits.maxResources} />
          <p className="muted">单次工作 {formatBytes(usage.limits.maxBytes)} · {(usage.limits.maxPixels / 1e6).toFixed(0)} 百万像素 · 输出 {formatBytes(usage.limits.maxOutputBytes)}。这些不是整个浏览器的硬内存上限。</p>
        </section> : null}
      </div> : null}

      {inspector === 'operations' ? <div className="inspector" role="tabpanel" id="panel-operations" aria-labelledby="tab-operations">
        <div className="quick-search">
          <Icon name="search" size={15} />
          <input aria-label="快速查找能力" placeholder={`在 ${catalog.length} 个 Go 入口中查找`} value={quickSearch} onChange={(event) => setQuickSearch(event.target.value)}
            onKeyDown={(event) => { if (event.key === 'Enter' && quickMatches[0]) { selectOperation(quickMatches[0].id); setQuickSearch(''); } }} />
          {quickMatches.length ? <ul className="quick-search__list" role="listbox" aria-label="匹配的能力">
            {quickMatches.map((item) => <li key={item.id}><button type="button" onClick={() => { selectOperation(item.id); setQuickSearch(''); }}>
              <span>{labels[item.label] || item.label}</span><code>{item.id}</code></button></li>)}
          </ul> : null}
        </div>
        <label className="field"><span>分类</span><select value={group} onChange={(event) => { setGroup(event.target.value); const first = catalog.find((item) => item.group === event.target.value); if (first) setOperationID(first.id); }}>{groups.map((name) => <option key={name}>{name}</option>)}</select></label>
        <label className="field"><span>操作</span><select value={operationID} onChange={(event) => setOperationID(event.target.value)}>{filteredOperations.map((item) => <option key={item.id} value={item.id}>{labels[item.label] || item.label}</option>)}</select></label>
        {operation ? <section className="contract">
          <code className="contract__api">{operation.goAPI}</code>
          {operation.description ? <p>{operation.description}</p> : null}
          {operation.parameters.length ? <ul className="contract__params">{operation.parameters.map((parameter) => <li key={parameter.name}><span>{parameter.name}{parameter.optional ? '?' : ''}</span><code>{parameter.type}</code></li>)}</ul> : null}
        </section> : null}
        {operation?.targetKind ? <label className="field"><span>目标资源</span><select value={target} onChange={(event) => setTarget(event.target.value)}><option value="">选择 {operation.targetKind} 资源</option>{targetOptions.map((item) => <option key={item.id} value={item.id}>{item.id} · {item.kind}</option>)}</select></label> : null}
        {(operation?.parameters || []).map((parameter, index) => {
          if (parameter.type === 'psdedit.LayerID' || ['layerIndex', 'index'].includes(parameter.name) && parameter.type.includes('int')) return <label className="field" key={parameter.name}><span>{parameter.name}</span><select value={currentArgs[index] ?? ''} onChange={(event) => updateArgument(index, Number(event.target.value))}>{layers.filter((item) => parameter.type === 'psdedit.LayerID' || item.SourceIndex >= 0).map((layer) => <option key={layer.ID} value={parameter.type === 'psdedit.LayerID' ? layer.ID : layer.SourceIndex}>{layer.Name}</option>)}</select></label>;
          if (parameter.type === '*image.NRGBA' || parameter.type === '*image.Alpha' || parameter.type === '[]uint8') return <label className="field" key={parameter.name}><span>{parameter.name}</span><select value={currentArgs[index]?.$asset || ''} onChange={(event) => updateArgument(index, { $asset: event.target.value })}><option value="">选择本机资源</option>{assets.filter((asset) => asset.kind !== 'document').map((asset) => <option key={asset.id} value={asset.id}>{asset.name}</option>)}</select></label>;
          if (['string', 'bool'].includes(parameter.type) || /^(u?int\d*|float\d+)$/.test(parameter.type)) return <label className="field" key={parameter.name}><span>{parameter.name}</span>{parameter.type === 'bool' ? <input type="checkbox" checked={!!currentArgs[index]} onChange={(event) => updateArgument(index, event.target.checked)} /> : <input value={currentArgs[index] ?? ''} type={parameter.type === 'string' ? 'text' : 'number'} onChange={(event) => updateArgument(index, parameter.type === 'string' ? event.target.value : Number(event.target.value))} />}</label>;
          return null;
        })}
        <label className="field"><span>单次预算</span><div className="unit-input"><input type="number" min="16" max="256" value={budget} onChange={(event) => setBudget(Math.max(16, Math.min(256, Number(event.target.value))))} /><span>MiB</span></div></label>
        {operation?.policy ? <div className="policy"><Icon name="warning" size={16} /><span>{operation.policy}</span></div> : null}
        <button type="button" className="run-btn" disabled={isBusy || !ready || !operation} onClick={() => run()}><Icon name="play" size={15} />{busy ? '执行中…' : '运行操作'}</button>
        <details className="advanced"><summary><Icon name="chevronRight" size={14} />高级参数（JSON）</summary>
          <label className="advanced__label" htmlFor="advanced-args">参数数组，按上方 Go 参数顺序</label>
          <textarea id="advanced-args" spellCheck="false" value={argumentsText} onChange={(event) => setArgumentsText(event.target.value)} />
          <small>使用 {'{"$asset":"a…"}'} 引用本机文件，{'{"$ref":"r…"}'} 引用类型化结果，{'{"$int64":"…"}'} 传递大整数。</small>
        </details>
        <details className="advanced"><summary><Icon name="chevronRight" size={14} />外链资源映射</summary>
          <textarea aria-label="外链映射 JSON" spellCheck="false" className="short" value={linksText} onChange={(event) => setLinksText(event.target.value)} />
          <small>原始 ID / 名称 → 本机 asset ID。不读取文件中记录的系统路径或 URL。</small>
        </details>
      </div> : null}

      {inspector === 'results' ? <div className="inspector" role="tabpanel" id="panel-results" aria-labelledby="tab-results">
        <section className="ins-section">
          <h3>最近一次结果</h3>
          <div className="downloads">{result?.binaries?.filter((item) => item.mime !== 'application/x-rgba8').map((item) => <button type="button" key={item.id} onClick={() => download(item)}><Icon name="download" size={14} /><span>{item.name}</span><small>{formatBytes(item.length)}</small></button>)}
            {!result?.binaries?.length ? <p className="empty">没有二进制输出</p> : null}</div>
          <pre className="json json--tall">{JSON.stringify(result?.data || {}, null, 2)}</pre>
        </section>
        <section className="ins-section">
          <h3>资源句柄 <small>{resources.length}</small></h3>
          <ul className="handles">{resources.map((item) => <li key={item.id}><code>{item.id}</code><span>{item.kind}</span><small title={item.type}>{item.type}</small>
            <button type="button" className="text-btn" disabled={isBusy} onClick={() => run(op('resource.release'), [], item.id, `释放 ${item.id}`)} aria-label={`释放 ${item.id}`}>释放</button></li>)}</ul>
          {!resources.length ? <p className="empty">没有打开的句柄</p> : null}
          <p className="muted">释放根文档会一并清理其派生句柄；资产仍被借用时 asset.remove 会被拒绝。</p>
        </section>
      </div> : null}
    </aside>

    <footer className="pg-status">
      <span><Icon name="lock" size={13} />文件只在本机处理，不上传</span>
      <span className="pg-status__doc">{doc ? `${doc.header.Width} × ${doc.header.Height} · ${colorModes[doc.header.ColorMode] ?? doc.header.ColorMode} ${doc.header.Depth} 位 · ${doc.header.Version === 2 ? 'PSB' : 'PSD'}` : '无文档'}</span>
      <span>{usage ? `资产 ${formatBytes(usage.assetBytes)} · 句柄 ${resources.length}` : ''}</span>
      <span>{version ? `Go Wasm ${version}` : ''}</span>
    </footer>

    {/* ───────── 对话框 ───────── */}
    {dialog?.type === 'confirm' ? <Dialog title={dialog.title} tone={dialog.tone || 'warn'} onClose={() => setDialog(null)} actions={<>
      <button type="button" className="tb-btn" onClick={() => setDialog(null)}>取消</button>
      {dialog.tone !== 'danger' && doc?.dirty ? <button type="button" className="tb-btn" onClick={() => { setDialog(null); exportPSD(); }}><Icon name="download" size={14} />先导出 PSD</button> : null}
      <button type="button" className="tb-btn tb-btn--danger-solid" onClick={() => { const action = dialog.action; setDialog(null); action(); }}>继续</button>
    </>}><p>{dialog.message}</p></Dialog> : null}

    {dialog?.type === 'new' ? <Dialog title="新建空白文档" onClose={() => setDialog(null)} actions={<>
      <button type="button" className="tb-btn" onClick={() => setDialog(null)}>取消</button>
      <button type="button" className="tb-btn tb-btn--primary" onClick={() => { const { width, height, version: kind } = dialog; setDialog(null); run(op('document.create'), [width, height, kind], '', '新建文档'); }}>创建</button>
    </>}>
      <div className="form-grid">
        <label className="field"><span>宽度</span><div className="unit-input"><input type="number" min="1" max="30000" value={dialog.width} onChange={(event) => setDialog({ ...dialog, width: Number(event.target.value) })} /><span>px</span></div></label>
        <label className="field"><span>高度</span><div className="unit-input"><input type="number" min="1" max="30000" value={dialog.height} onChange={(event) => setDialog({ ...dialog, height: Number(event.target.value) })} /><span>px</span></div></label>
        <label className="field"><span>格式</span><select value={dialog.version} onChange={(event) => setDialog({ ...dialog, version: Number(event.target.value) })}><option value={1}>PSD（版本 1）</option><option value={2}>PSB（版本 2，大文档）</option></select></label>
      </div>
      <p className="muted">透明 RGB8 文档，由 document.create 调用 psdkit.CreateDocument 生成。尺寸受工作区像素预算限制。</p>
    </Dialog> : null}

    {dialog?.type === 'group' ? <Dialog title="新建图层组" onClose={() => setDialog(null)} actions={<>
      <button type="button" className="tb-btn" onClick={() => setDialog(null)}>取消</button>
      <button type="button" className="tb-btn tb-btn--primary" onClick={() => { const placement = placeAbove(doc.layers, activeLayer); setDialog(null); editLayer('editor.AddGroup', [placement.parent, placement.position, dialog.name, true], '新建图层组'); }}>创建</button>
    </>}>
      <label className="field"><span>名称</span><input value={dialog.name} onChange={(event) => setDialog({ ...dialog, name: event.target.value })} /></label>
      <p className="muted">创建在{activeLayer?.Group ? `组“${activeLayer.Name}”内的顶部` : '当前选中图层的上方'}（editor.AddGroup）。</p>
    </Dialog> : null}

    {dialog?.type === 'raster' ? <Dialog title="添加图片图层" onClose={() => setDialog(null)} actions={<>
      <button type="button" className="tb-btn" onClick={() => setDialog(null)}>取消</button>
      <button type="button" className="tb-btn tb-btn--primary" disabled={!dialog.asset} onClick={() => { const placement = placeAbove(doc.layers, activeLayer); setDialog(null); editLayer('editor.AddRaster', [placement.parent, placement.position, dialog.name, { $asset: dialog.asset }, { X: dialog.x, Y: dialog.y }], '添加图片图层'); }}>添加</button>
    </>}>
      <div className="form-grid">
        <label className="field"><span>图片</span><select value={dialog.asset} onChange={(event) => setDialog({ ...dialog, asset: event.target.value })}>{imageAssets.map((asset) => <option key={asset.id} value={asset.id}>{asset.name}</option>)}</select></label>
        <label className="field field--wide"><span>名称</span><input value={dialog.name} onChange={(event) => setDialog({ ...dialog, name: event.target.value })} /></label>
        <label className="field"><span>X</span><div className="unit-input"><input type="number" value={dialog.x} onChange={(event) => setDialog({ ...dialog, x: Number(event.target.value) })} /><span>px</span></div></label>
        <label className="field"><span>Y</span><div className="unit-input"><input type="number" value={dialog.y} onChange={(event) => setDialog({ ...dialog, y: Number(event.target.value) })} /><span>px</span></div></label>
      </div>
      <p className="muted">PNG/JPEG 以 RGB8 插入（editor.AddRaster），保留 alpha；16 位图片不会被静默降位。</p>
    </Dialog> : null}
  </div>;
}

// 新图层放在选中图层上方；选中组时放进组内顶部。同级按自下而上计数。
function placeAbove(tree, layer) {
  if (!layer) return { parent: 0, position: (tree || []).length };
  if (layer.Group) return { parent: layer.ID, position: (layer.Children || []).length };
  const siblings = layer.Parent ? findLayer(tree, layer.Parent)?.Children || [] : tree || [];
  const index = siblings.findIndex((item) => item.ID === layer.ID);
  return { parent: layer.Parent || 0, position: index + 1 };
}

function LayerProperties({ layer, busy, imageAssets, onRename, onVisible, onOpacity, onFill, onReplace, onRemove, onOperations, onImport,
  smart, apparel, contentAssets, onReplaceContents }) {
  const [chosenContents, setContents] = useState('');
  const contents = contentAssets.some((item) => item.id === chosenContents) ? chosenContents : contentAssets[0]?.id || '';
  const [name, setName] = useState(layer.Name);
  const [opacity, setOpacity] = useState(layer.Opacity);
  const [fill, setFill] = useState(layer.FillOpacity);
  const [chosenAsset, setAsset] = useState('');
  const asset = imageAssets.some((item) => item.id === chosenAsset) ? chosenAsset : imageAssets[0]?.id || '';
  const bounds = layer.Bounds ? `${layer.Bounds.Min.X}, ${layer.Bounds.Min.Y} → ${layer.Bounds.Max.X}, ${layer.Bounds.Max.Y}` : '—';
  const size = layer.Bounds ? `${layer.Bounds.Max.X - layer.Bounds.Min.X} × ${layer.Bounds.Max.Y - layer.Bounds.Min.Y}` : '—';
  const commitName = () => { if (name.trim() && name !== layer.Name) onRename(name.trim()); };
  return <section className="ins-section ins-section--layer">
    <div className="layer-card">
      <span className={`layer-card__thumb ${layer.Group ? 'is-group' : ''}`}><Icon name={layer.Group ? 'folder' : 'image'} size={18} /></span>
      <div><small>{layer.Group ? '图层组' : '图层'} · LayerID {layer.ID}{layer.SourceIndex >= 0 ? ` · 源序号 ${layer.SourceIndex}` : ' · 新建'}</small>
        <input className="layer-card__name" aria-label="图层名称" value={name} disabled={busy} onChange={(event) => setName(event.target.value)}
          onBlur={commitName} onKeyDown={(event) => { if (event.key === 'Enter') event.currentTarget.blur(); if (event.key === 'Escape') setName(layer.Name); }} /></div>
    </div>
    <div className="prop-row"><span>可见</span>
      <button type="button" role="switch" aria-checked={layer.Visible} className="switch" disabled={busy} onClick={onVisible} aria-label="图层可见性"><i /></button></div>
    <Slider label="不透明度" value={opacity} onChange={setOpacity} onCommit={onOpacity} disabled={busy} />
    <Slider label="填充" value={fill} onChange={setFill} onCommit={onFill} disabled={busy || layer.Group} />
    <dl className="facts facts--compact">
      <div><dt>位置</dt><dd>{bounds}</dd></div>
      <div><dt>尺寸</dt><dd>{size}</dd></div>
      {layer.PSDID ? <div><dt>PSD ID</dt><dd>{layer.PSDID}</dd></div> : null}
    </dl>
    {smart ? <div className="prop-block prop-block--smart">
      <span className="prop-block__title"><Icon name="box" size={14} />智能对象 <small>{smart.Embedded ? '嵌入来源' : '外链来源'} · {smart.SourceKind}{smart.Replaced ? ' · 已替换' : ''}</small></span>
      <dl className="facts facts--compact">
        <div><dt>来源尺寸</dt><dd>{Math.round(smart.Width)} × {Math.round(smart.Height)}</dd></div>
        <div><dt>共享实例</dt><dd>{smart.SharedInstances?.length || 1}</dd></div>
      </dl>
      {contentAssets.length ? <div className="inline-form"><select aria-label="替换内容来源" value={contents} onChange={(event) => setContents(event.target.value)} disabled={busy}>
        {contentAssets.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
        <button type="button" className="tb-btn tb-btn--primary" disabled={busy || !contents} onClick={() => onReplaceContents(contents)}>替换内容</button></div>
        : <button type="button" className="tb-btn tb-btn--block" disabled={busy} onClick={onImport}><Icon name="upload" size={14} />导入替换图片</button>}
      <small className="prop-block__note">{apparel ? '此上衣样机使用显式数学 Warp 近似，印花按 sRGB 处理，5 个共享衣片联动。' : ''}图片转为可编辑 PSD 后替换真实来源，保留放置、蒙版与 Warp；未支持的几何或滤镜仍会被拒绝。</small>
    </div> : null}
    {!layer.Group && !smart ? <div className="prop-block">
      <span className="prop-block__title">替换像素 <small>同尺寸 RGB8（editor.ReplacePixels）</small></span>
      {imageAssets.length ? <div className="inline-form"><select aria-label="替换用图片" value={asset} onChange={(event) => setAsset(event.target.value)} disabled={busy}>{imageAssets.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
        <button type="button" className="tb-btn" disabled={busy || !asset} onClick={() => onReplace(asset)}>替换</button></div>
        : <button type="button" className="tb-btn tb-btn--block" disabled={busy} onClick={onImport}><Icon name="upload" size={14} />导入 PNG / JPEG</button>}
    </div> : null}
    <div className="prop-actions">
      <button type="button" className="tb-btn" disabled={busy} onClick={onOperations}><Icon name="code" size={14} />更多图层操作</button>
      <button type="button" className="tb-btn tb-btn--danger" disabled={busy} onClick={onRemove}><Icon name="trash" size={14} />删除</button>
    </div>
  </section>;
}

function Slider({ label, value, onChange, onCommit, disabled }) {
  const percent = Math.round(value / 2.55);
  const committed = useRef(value);
  // pointerup、按键和失焦都可能触发提交；同一数值只提交一次。
  const commit = (event) => {
    const next = Number(event.currentTarget.value);
    if (next === committed.current) return;
    committed.current = next;
    onCommit(next);
  };
  return <label className="slider">
    <span className="slider__row"><span>{label}</span><output>{percent}%<small> · {value}</small></output></span>
    <input type="range" min="0" max="255" value={value} disabled={disabled} aria-label={label} aria-valuetext={`${percent}%（${value} / 255）`}
      onChange={(event) => onChange(Number(event.target.value))} onPointerUp={commit} onKeyUp={(event) => { if (/Arrow|Page|Home|End/.test(event.key)) commit(event); }} onBlur={commit} />
  </label>;
}
