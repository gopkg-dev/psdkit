export function unwrap(reply) {
  const first = reply?.data?.results?.[0];
  return first && Object.hasOwn(first, 'value') ? first.value : first ?? reply?.data;
}

export function flattenLayers(nodes, depth = 0) {
  return [...(nodes || [])].reverse().flatMap((layer) => [
    { ...layer, depth }, ...flattenLayers(layer.Children, depth + 1),
  ]);
}

export function imageResults(reply) {
  const found = [];
  function visit(value) {
    if (!value || typeof value !== 'object') return;
    if (value.image?.binary) found.push(value);
    for (const child of Object.values(value)) if (child && typeof child === 'object') visit(child);
  }
  visit(reply?.data);
  return found;
}

export const labels = {
  Tree: '图层树', Layers: '所有图层', Layer: '图层信息', Changes: '修改记录', Rename: '重命名图层',
  SetVisible: '显示 / 隐藏图层', SetOpacity: '图层不透明度', SetFillOpacity: '填充不透明度',
  SetFillOpacityContext: '填充不透明度', ReplacePixels: '替换图层像素', ReplaceChannel: '替换源精度通道',
  AddRaster: '添加图片图层', AddGroup: '添加图层组', Move: '移动图层', Remove: '删除图层',
  SmartObjects: '智能对象', ReplaceContents: '替换智能对象内容', ReplaceSmartObjectContents: '替换共享内容',
  ReplaceContentsWithOptions: '按显式策略替换智能对象',
  SetPatternFill: '编辑图案填充', SetGradientFill: '编辑渐变', SetSolidFill: '编辑纯色填充',
  SetAdjustment: '编辑调整图层', SetVectorMask: '编辑矢量蒙版', SetEffects: '编辑图层效果',
  SetEffectsWithOptions: '编辑效果与近似策略', SetSmartFilterParameters: '编辑智能滤镜参数',
  SetTextContent: '编辑文字内容与准确像素', SetNativeTextState: '导入原生文字状态',
  EmbedLinkedContents: '外链转嵌入', UpdateLinkedContents: '更新外链内容', OpenSmartObjectSource: '打开智能对象源',
  Write: '导出原格式 PSD / PSB', WritePSD: '导出 PSD / PSB', Composite: '合成编辑后的文档',
  CompositeGroup: '独立合成图层组',
  ReadLayerMetadata: '读取图层语义', LayerMetadata: '图层元数据', ReadSmartObjectView: '智能对象与资格',
  DecodeLayer: '解码原始图层', DecodeComposite: '解码保存的源精度合成图', DecodeChannel: '解码源精度通道',
  DecodeChannelRegion: '按区域解码通道', DecodeCompositeChannelRegion: '按区域解码保存合成图',
  Decode: '解码 RGB8 文档与保存预览', DecodeLayers: '仅解码 RGB8 图层', ReadSpan: '读取原始数据块',
  CopySpan: '流式导出原始数据块', CopySpanBuffer: '指定缓冲导出数据块', Limits: '读取当前预算',
  IndexMetadataBytes: '索引内存', LinkedFiles: '外链 / 嵌入记录', OpenLinkedDocument: '打开嵌入 PSD / PSB',
  ResolveLinkedDocument: '解析显式外链', DecodeLinkedRaster: '解码嵌入图片', LinkedRasterFormat: '嵌入图片格式',
  ReadLinkedFileView: '所有嵌入与外链记录', TextLayers: '文字层与原始描述符', DecodeTextEngine: '解析文字 EngineData',
  DecodeEngineData: '解析原始排版数据', ReadTextLayerView: '文字元数据与预算',
  ReadDocumentTextView: '文档文字数据', ReadDocumentTextAssociations: '文字层关联',
  DecodeDocumentTextEngine: '文档级文字引擎', DecodeGradientFill: '解析渐变', DecodePatternFill: '解析图案填充',
  ReadPatternView: '图案资源', Patterns: '读取图案记录', DecodePattern: '解码源精度图案',
  DecodePatternTileRGB8: '解码 RGB8 图案', EstimatePatternTileRGB8: '图案解码预算', ResolvePattern: '关联图案资源',
  DecodeEffects: '解析图层效果', DecodeAdjustment: '解析调整图层', DecodeVectorMask: '解码矢量蒙版',
  DecodePath: '解码路径', DecodePathResource: '解码路径资源', DecodeVectorMaskBlock: '解码矢量块',
  DecodeDescriptor: '解析原始描述符', DecodeDescriptorContext: '受控解析描述符', EncodeDescriptor: '重编码描述符',
  FilterCaches: '智能滤镜缓存结构', ReadFilterCacheView: '滤镜缓存索引', DecodeFilterCacheRegion: '显式读取缓存区域',
  EncodeSmartFilterParameters: '编码滤镜参数', Artboards: '画板记录', DecodeArtboard: '解析画板',
  Forward: '正向坐标', Inverse: '逆向坐标', Bounds: '几何边界', Project: '图像投影',
  ProjectWithOptions: '指定预算投影', ProjectSupersampled: '实验性超采样投影', WorkingBytes: '工作内存预算',
  Glyph: '字形轮廓', LookupRune: 'Unicode 字符查找', Kerning: '字偶距', VariationAxes: '可变字体轴',
  VariationCoordinates: '实例坐标', NamedVariationInstances: '命名实例', VariationMetrics: '可变字体指标',
  InstanceMetrics: '实例字形指标', Stats: '区域缓存统计', Clear: '清空区域缓存',
};

export function defaultArgs(operation, document, layer, assets, fonts) {
  return (operation?.parameters || []).map((parameter) => {
    if (parameter.type === '*psdfile.Document') return { $ref: document?.document || '' };
    if (parameter.type === 'psdedit.LayerID') return layer?.ID || 1;
    if (['layerIndex', 'index', 'layer'].includes(parameter.name) && parameter.type.includes('int')) return layer?.SourceIndex ?? 0;
    if (parameter.type === '*image.NRGBA' || parameter.type === '*image.Alpha' || parameter.type === '[]uint8') {
      const kind = operation.id.startsWith('font.') ? 'font' : 'image';
      return { $asset: assets.find((asset) => asset.kind === kind)?.id || '' };
    }
    if (parameter.type === 'image.Rectangle') return {
      Min: { X: 0, Y: 0 }, Max: { X: document?.header?.Width || 640, Y: document?.header?.Height || 512 },
    };
    if (parameter.type === 'psdfont.Style') return { FontName: fonts[0]?.name || '', Size: 48, Kerning: true };
    return parameter.example;
  });
}

// 结构化错误码的中文说明：保留原始 message / path，不改写库的判断。
export const errorHelp = {
  unsupported: { title: '超出支持范围', hint: '该文档或操作超出 psdkit 当前支持范围。库拒绝输出，不返回替代图像；可改用保存预览查看文件内的合成图。' },
  resource_limit: { title: '超出资源预算', hint: '超出浏览器工作区预算（单次工作 256 MiB、1600 万像素等）。可缩小区域、降低单次预算，或在原生 Go 中处理大型文档。' },
  missing_font: { title: '缺少字体', hint: '库不会读取系统字体或自动替换。请导入对应的 TTF / OTF / TTC，并在 font.decode 中设置 alias 对应 PSD 的 PostScript 名称。' },
  missing_glyph: { title: '缺少字形', hint: '已提供的字体不包含所需字形。请导入覆盖这些字符的字体。' },
  invalid_request: { title: '请求无效', hint: '参数、句柄或资产无效。检查高级参数 JSON、目标资源和引用的 $asset / $ref。' },
  canceled: { title: '已取消或超时', hint: '操作被取消或超过 Go deadline（默认 60 秒）。' },
  worker_timeout: { title: 'Worker 超时', hint: 'SDK 看门狗超时，Worker 已终止，工作区与未导出的编辑已释放。重新启动后需要重新导入文件。' },
  worker_error: { title: 'Worker 运行错误', hint: 'Worker 或 Wasm 运行时出错。可重新启动引擎；如持续出现，请检查 wasm/ 资源是否完整加载。' },
  operation_failed: { title: '操作失败', hint: '原 Go 库返回了错误，详情见下方原始信息。' },
};

export function explain(error) {
  return errorHelp[error?.code] || errorHelp.operation_failed;
}

export function formatBytes(bytes = 0) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(bytes < 10 * 1024 * 1024 ? 2 : 1)} MiB`;
}

export const colorModes = { 0: 'Bitmap', 1: '灰度', 2: '索引', 3: 'RGB', 4: 'CMYK', 7: '多通道', 8: '双色调', 9: 'Lab' };

export function assetKind(name) {
  const extension = name.split('.').pop().toLowerCase();
  if (['psd', 'psb'].includes(extension)) return 'document';
  if (['ttf', 'otf', 'ttc'].includes(extension)) return 'font';
  if (['icc', 'icm'].includes(extension)) return 'profile';
  return 'image';
}

export function findLayer(nodes, id) {
  for (const node of nodes || []) {
    if (node.ID === id) return node;
    const found = findLayer(node.Children, id);
    if (found) return found;
  }
  return null;
}

// 编辑后需要刷新图层树并实时重绘的编辑器入口（只读与导出入口除外）。
export const readOnlyEditorOps = new Set(['editor.Tree', 'editor.Layers', 'editor.Layer', 'editor.Changes', 'editor.Write',
  'editor.WritePSD', 'editor.Composite', 'editor.CompositeGroup', 'editor.SmartObjects', 'editor.OpenSmartObjectSource']);
