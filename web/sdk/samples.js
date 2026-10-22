// Shared, checksum-verified apparel sample loading for the homepage and Playground.
export async function loadGarmentSample(engine, baseURL) {
  const root = new URL(baseURL, globalThis.location?.href);
  const response = await fetch(new URL('manifest.json', root));
  if (!response.ok) throw new Error(`示例清单加载失败：HTTP ${response.status}`);
  const manifest = await response.json();
  if (manifest.version !== 1 || !manifest.document?.file || manifest.prints?.length !== 2) {
    throw new Error('上衣示例清单不完整');
  }
  const contents = manifest.prints.map((item) => item.contents).filter(Boolean);
  const items = [manifest.document, ...manifest.prints, ...contents];
  const buffers = await Promise.all(items.map(async (item) => {
    const file = await fetch(new URL(item.file, root));
    if (!file.ok) throw new Error(`${item.file}：HTTP ${file.status}`);
    const bytes = await file.arrayBuffer();
    const hash = await crypto.subtle.digest('SHA-256', bytes);
    const actual = [...new Uint8Array(hash)].map((value) => value.toString(16).padStart(2, '0')).join('');
    if (actual !== item.sha256) throw new Error(`${item.file}：示例文件校验失败`);
    if (item.compression === 'gzip') {
      return new Response(new Blob([bytes]).stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer();
    }
    if (item.compression) throw new Error(`${item.file}：不支持的示例压缩格式`);
    return bytes;
  }));
  const assetIDs = [];
  let opened;
  try {
    for (let index = 0; index < items.length; index++) {
      const item = items[index];
      const put = await engine.call('asset.put', {
        bytes: buffers[index], name: index === 0 ? item.file
          : index <= manifest.prints.length ? `${item.name}.png` : item.name,
        kind: index === 0 ? 'document' : index <= manifest.prints.length ? 'image' : 'contents',
      });
      assetIDs.push(put.data.asset);
    }
    opened = await engine.call('document.open', { args: [{ $asset: assetIDs[0] }] });
    return {
      opened, manifest, asset: assetIDs[0],
      prints: manifest.prints.map((item, index) => ({ ...item, asset: assetIDs[index + 1],
        contentsAsset: item.contents ? assetIDs[items.indexOf(item.contents)] : undefined,
      })),
    };
  } catch (error) {
    if (opened?.data?.document) {
      await engine.call('resource.release', { target: opened.data.document }).catch(() => {});
    }
    for (const target of assetIDs.reverse()) {
      await engine.call('asset.remove', { target }).catch(() => {});
    }
    throw error;
  }
}
