// Contain uploaded artwork on the same white source canvas used by the bundled prints.
export async function normalizePrint(file, { width = 1000, height = 823 } = {}) {
  if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type)) throw new Error('请选择 PNG、JPEG 或 WebP 图片');
  if (file.size > 20 * 1024 * 1024) throw new Error('印花文件不能超过 20 MB');
  const url = URL.createObjectURL(file);
  try {
    const image = new Image();
    image.src = url;
    await image.decode();
    if (image.naturalWidth * image.naturalHeight > 20_000_000) throw new Error('印花不能超过 2000 万像素');
    const canvas = document.createElement('canvas');
    canvas.width = width; canvas.height = height;
    const context = canvas.getContext('2d');
    context.fillStyle = 'white'; context.fillRect(0, 0, width, height);
    const scale = Math.min(width * .92 / image.naturalWidth, height * .92 / image.naturalHeight);
    const w = image.naturalWidth * scale, h = image.naturalHeight * scale;
    context.drawImage(image, (width - w) / 2, (height - h) / 2, w, h);
    return await new Promise((resolve, reject) => canvas.toBlob((blob) => {
      if (blob) resolve(blob); else reject(new Error('无法生成印花 PNG'));
    }, 'image/png'));
  } finally { URL.revokeObjectURL(url); }
}

// BuildRasterContents returns a retained handle which must be released before its input asset.
export async function replacePrintContents(engine, doc, print, policy) {
  if (print.contentsAsset) {
    // Bundled contents are checksum-verified build outputs, borrowed until the workspace closes.
    await engine.call('editor.ReplaceContentsWithOptions', {
      target: doc.editor, args: [doc.smart, { $asset: print.contentsAsset }, policy],
    });
    return;
  }
  const assets = [], resources = [];
  let operationError;
  try {
    let imageAsset = print.asset;
    if (print.blob) {
      const uploaded = await engine.call('asset.put', { bytes: await print.blob.arrayBuffer(), name: print.name, kind: 'image' });
      imageAsset = uploaded.data.asset; assets.push(imageAsset);
    }
    const built = await engine.call('raster.buildPSD', { args: [{ $asset: imageAsset }, {}] });
    resources.push(...(built.data?.results || []).map((item) => item.handle).filter(Boolean));
    if (!built.binaries?.[0]) throw new Error('引擎没有返回印花 PSD');
    const contents = await engine.call('asset.put', { bytes: built.binaries[0].data, name: 'contents.psd', kind: 'contents' });
    assets.push(contents.data.asset);
    await engine.call('editor.ReplaceContentsWithOptions', {
      target: doc.editor, args: [doc.smart, { $asset: contents.data.asset }, policy],
    });
  } catch (error) {
    operationError = error;
    throw error;
  } finally {
    let cleanupError;
    const pending = [
      ...resources.reverse().map((target) => ['resource.release', target]),
      ...assets.reverse().map((target) => ['asset.remove', target]),
    ];
    for (const [operation, target] of pending) {
      try { await engine.call(operation, { target }); }
      catch (error) { cleanupError ??= error; }
    }
    if (cleanupError && !operationError) throw cleanupError;
  }
}
