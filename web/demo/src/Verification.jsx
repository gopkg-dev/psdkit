import React, { useEffect, useRef, useState } from 'react';
import { PsdkitClient } from '../../sdk/client.js';

async function sha256(buffer) {
  const digest = await crypto.subtle.digest('SHA-256', buffer);
  return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, '0')).join('');
}

export function Verification({ onBusy, log, cancelRef, disabled }) {
  const [manifest, setManifest] = useState(null);
  const [rows, setRows] = useState([]);
  const [message, setMessage] = useState('');
  const [running, setRunning] = useState(false);
  const active = useRef(null);

  useEffect(() => {
    cancelRef.current = () => active.current?.close();
    fetch('/__fixtures/manifest').then((response) => {
      if (!response.ok || !response.headers.get('content-type')?.includes('application/json')) return null;
      return response.json();
    }).then(setManifest).catch(() => {});
    return () => { active.current?.close(); cancelRef.current = null; };
  }, []);

  async function execute() {
    if (!manifest) return;
    setRunning(true); onBusy(true); setRows([]); setMessage('启动独立 Worker，主工作区保持独立。');
    const engine = new PsdkitClient(); active.current = engine;
    let heartbeats = 0, maximumGap = 0, previous = performance.now();
    const heartbeat = setInterval(() => {
      const now = performance.now(); maximumGap = Math.max(maximumGap, now - previous); previous = now; heartbeats++;
    }, 16);
    const start = performance.now();
    try {
      await engine.ready();
      for (const fixture of manifest.cases) {
        setMessage(`正在检查 ${fixture.id}`);
        const form = new FormData();
        let record = { id: fixture.id, browser: navigator.userAgent };
        const targetBytes = await (await fetch(fixture.targetURL)).arrayBuffer();
        const sourceBytes = await (await fetch(fixture.sourceURL)).arrayBuffer();
        if (await sha256(targetBytes) !== fixture.targetSHA || await sha256(sourceBytes) !== fixture.sourceSHA) {
          throw new Error(`输入 SHA 不匹配：${fixture.id}`);
        }
        const asset = await engine.importFile(new File([targetBytes], `${fixture.id}.psd`), 'document');
        const image = await engine.importFile(new File([sourceBytes], `${fixture.id}.png`), 'image');
        const opened = await engine.call('document.open', { args: [{ $asset: asset.data.asset }] });
        const source = opened.data;
        record.header = source.header;
        const metadata = await engine.call('document.ReadLayerMetadata', { target: source.document, args: [fixture.layerIndex, {}] });
        record.metadataRead = metadata.ok;
        try {
          const rendered = await engine.call('warp.uniform.project', { args: [
            { $ref: source.document }, fixture.layerIndex, { $asset: image.data.asset },
            { Min: { X: 0, Y: 0 }, Max: { X: fixture.width, Y: fixture.height } }, {},
          ] });
          const output = rendered.binaries.find((item) => item.mime === 'image/png');
          record.rendered = true;
          record.projectionAudit = rendered.data.results[1]?.value || rendered.data.results[1];
          form.append('png', new Blob([output.data], { type: 'image/png' }), 'browser.png');
        } catch (problem) { record.rendered = false; record.renderError = { code: problem.code, message: problem.message, audit: problem.audit }; }
        const saved = await engine.call('editor.WritePSD', { target: source.editor, args: [{}] });
        const psd = saved.binaries.find((item) => item.mime === 'image/vnd.adobe.photoshop');
        record.exported = !!psd;
        form.append('psd', new Blob([psd.data], { type: psd.mime }), 'browser.psd');
        const roundAsset = await engine.call('asset.put', { bytes: psd.data.slice(0), name: 'roundtrip.psd', kind: 'document' });
        const reopened = await engine.call('document.open', { args: [{ $asset: roundAsset.data.asset }] });
        record.reopenedHeader = reopened.data.header;
        await engine.call('resource.release', { target: reopened.data.document });
        await engine.call('asset.remove', { target: roundAsset.data.asset });
        await engine.call('resource.release', { target: source.document });
        await engine.call('asset.remove', { target: asset.data.asset });
        await engine.call('asset.remove', { target: image.data.asset });
        record.heartbeats = heartbeats;
        form.append('record', JSON.stringify(record));
        const response = await fetch('/__fixtures/result', { method: 'POST', body: form });
        const compared = await response.json();
        if (!response.ok) throw new Error(compared.error || '对照服务拒绝结果');
        setRows((current) => [...current, compared]);
      }
      const response = await fetch('/__fixtures/complete', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ heartbeats, maximumMainThreadGapMS: maximumGap, elapsedMS: performance.now() - start, browser: navigator.userAgent }) });
      const completed = await response.json();
      if (!response.ok) throw new Error(completed.error || '回归不完整');
      setMessage(`完成 ${completed.cases} 例；${completed.supported} 个输出、${completed.rejected} 个明确拒绝。`);
      log('66 例浏览器回归', 'success', `${completed.cases} 例已保存到本地验收目录`, performance.now() - start);
    } catch (problem) { setMessage(`检查中断：${problem.message}`); log('浏览器回归', 'error', problem.message); }
    finally { clearInterval(heartbeat); engine.close(); active.current = null; setRunning(false); onBusy(false); }
  }

  async function fontCheck() {
    const engine = new PsdkitClient();
    active.current = engine;
    setRunning(true); onBusy(true); setMessage('验证字体、图片与 PSD 导出闭环…');
    try {
      await engine.ready();
      const reply = await fetch('/__fixtures/font');
      if (!reply.ok) throw new Error('没有配置本地字体测试文件');
      const asset = await engine.importFile(new File([await reply.arrayBuffer()], 'fixture.ttf'), 'font');
      const face = await engine.call('font.decode', { args: [{ $asset: asset.data.asset }, {}] });
      const fontName = face.data.results[0].value.PostScriptName;
      const layout = await engine.call('font.layout', { args: ['AV', { FontName: fontName, Size: 48 }, { OriginY: 60 }] });
      const runHandle = layout.data.results[0].handle;
      const raster = await engine.call('font.raster', { args: [{ $ref: runHandle }, { Min: { X: 0, Y: 0 }, Max: { X: 140, Y: 90 } }, {}] });
      const output = raster.binaries.find((item) => item.mime === 'image/png');
      if (!output) throw new Error('字形未生成 PNG');
      const proof = { fontName, glyphs: layout.data.results[0].value.Glyphs.length };
      const form = new FormData();
      form.append('record', JSON.stringify(proof));
      form.append('png', new Blob([output.data], { type: 'image/png' }), 'font-browser.png');
      const response = await fetch('/__fixtures/font-result', { method: 'POST', body: form });
      if (!response.ok) throw new Error('字体验证未通过');
      const compared = await response.json();
      if (compared.againstGo.Changed !== 0) throw new Error('字体像素与本机 Go 不一致');
      setMessage(`字体 ${fontName}：字形解析、排版、光栅 PNG 输出通过；与本机 Go 差异 0 px。`);
    } catch (problem) { setMessage(problem.message); }
    finally { engine.close(); active.current = null; setRunning(false); onBusy(false); }
  }

  return <div className="verification">
    <p>{manifest ? `本地验收集：${manifest.cases.length} 个冻结 PSD。Go Wasm 在独立 Worker 运行；输入 SHA、导出重开、读取守卫与像素由本地 Go 服务复算。` : '启动带 -fixtures 参数的本地 Go 服务后，可运行冻结的 66 份 PSD。普通 demo 不会访问本机测试目录。'}</p>
    <button className="primary" disabled={!manifest || running || disabled} onClick={execute}>运行 66 例浏览器对照</button>{' '}
    <button disabled={!manifest || running || disabled} onClick={fontCheck}>验证字体管线</button>{' '}
    {running ? <button onClick={() => active.current?.close()}>停止回归</button> : null}
    <p role="status" data-testid="verification-status">{message}</p>
    {rows.length ? <table className="log-table"><thead><tr><th>案例</th><th>状态</th><th>浏览器 / Go 差异</th><th>PS MAE</th></tr></thead><tbody>{rows.map((row) => <tr key={row.id}><td title={row.id}>{row.id}</td><td>{row.ok ? '通过' : '差异'}</td><td>{row.supported ? `${row.goChangedPixels} px · max ${row.goMax}` : '明确拒绝'}</td><td>{row.supported ? row.photoshopMAE?.toFixed(4) : '—'}</td></tr>)}</tbody></table> : null}
  </div>;
}
