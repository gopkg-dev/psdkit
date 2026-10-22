import copy
import hashlib
import json
from pathlib import Path

from fontTools.ttLib import TTFont
import uharfbuzz as hb

root = Path("/tmp/psdkit-font-shape-zero-controls")
if root.exists():
    raise ValueError("Zero-control experiment already exists")
root.mkdir()
source = Path("/tmp/psdkit-font-shape-own-fixtures/base.ttf")
original = source.read_bytes()
base = TTFont(source); base.recalcTimestamp = False
zero = copy.deepcopy(base)
for ligature in zero["GSUB"].table.LookupList.Lookup[0].SubTable[0].ligatures["f"]:
    if ligature.Component == ["i"]:
        ligature.LigGlyph = ".notdef"
matrix = copy.deepcopy(base)
class_table = matrix["GPOS"].table.LookupList.Lookup[0].SubTable[-1]
class_table.ValueFormat1 = class_table.ValueFormat2 = 0
for row in class_table.Class1Record:
    for cell in row.Class2Record:
        cell.Value1 = cell.Value2 = None
rows = []
for name, font in (("legal-ligature-glyph-zero", zero), ("zero-value-class-matrix", matrix)):
    path = root / (name + ".ttf"); font.save(path, reorderTables=False)
    data = path.read_bytes(); face = hb.Face(data); shaped_font = hb.Font(face); shaped_font.scale = (1000, 1000); hb.ot_font_set_funcs(shaped_font)
    cases = []
    for text in ("fi", "A", "fA", "fiAV"):
        for enabled in (False, True):
            buffer = hb.Buffer(); buffer.add_codepoints([ord(c) for c in text]); buffer.direction = "ltr"; buffer.script = "latn"; buffer.language = "en"
            hb.shape(shaped_font, buffer, {"liga": enabled, "kern": True, "ccmp": False, "calt": False, "locl": False, "rlig": False, "clig": False, "mark": False, "mkmk": False})
            infos = buffer.glyph_infos; ends = sorted(set(i.cluster for i in infos) | {len(text)})
            glyphs = [{"glyph_id": i.codepoint, "cluster_start_utf16": i.cluster,
                "cluster_length_utf16": next(end for end in ends if end > i.cluster) - i.cluster,
                "x_advance": p.x_advance, "y_advance": p.y_advance, "x_offset": p.x_offset, "y_offset": p.y_offset}
                for i, p in zip(infos, buffer.glyph_positions)]
            cases.append({"text": text, "liga": enabled, "kern": True, "glyphs": glyphs,
                "library_requires_missing_glyph_on_selected_match": any(g["glyph_id"] == 0 for g in glyphs)})
    rows.append({"variant": name, "path": str(path), "sha256": hashlib.sha256(data).hexdigest(), "cases": cases})
report = {"scope": "Fresh legal GSUB target glyph zero and zero-size PairPos class matrix controls; source font kept unchanged; no Go result claim.",
    "uharfbuzz_python_binding_version": hb.__version__, "harfbuzz_engine_version": hb.version_string(), "fonts": rows,
    "source_base_unchanged": original == source.read_bytes(), "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
Path("/tmp/psdkit-font-shape-zero-controls-oracle.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
for row in rows:
    print(row["variant"], row["sha256"], row["cases"])
