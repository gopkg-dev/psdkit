import copy
import hashlib
import json
from pathlib import Path

from fontTools.ttLib import TTFont
from fontTools.ttLib.tables import otTables
import uharfbuzz as hb

source = Path("/tmp/psdkit-font-shape-extra-features/named-language-without-liga.ttf")
before = source.read_bytes()
font = TTFont(source); font.recalcTimestamp = False
script = next(r.Script for r in font["GPOS"].table.ScriptList.ScriptRecord if r.ScriptTag == "latn")
record = otTables.LangSysRecord(); record.LangSysTag = "TRK "; record.LangSys = copy.deepcopy(script.DefaultLangSys)
script.LangSysRecord = [record]; script.LangSysCount = 1
path = Path("/tmp/psdkit-font-shape-named-TRK-both-tables.ttf")
if path.exists():
    raise ValueError("Named-language positive already exists")
font.save(path, reorderTables=False)
data = path.read_bytes(); face = hb.Face(data); selected = hb.Font(face); selected.scale = (1000, 1000); hb.ot_font_set_funcs(selected)
cases = []
for text in ("fi", "ffi", "fiAVAV"):
    for enabled in (False, True):
        buffer = hb.Buffer(); buffer.add_codepoints([ord(c) for c in text]); buffer.direction = "ltr"; buffer.script = "latn"; buffer.language = "tr"
        hb.shape(selected, buffer, {"liga": enabled, "kern": True, "ccmp": False, "locl": False, "rlig": False, "clig": False, "calt": False, "mark": False, "mkmk": False})
        ends = sorted(set(i.cluster for i in buffer.glyph_infos) | {len(text)})
        glyphs = [{"glyph_id": i.codepoint, "cluster_start_utf16": i.cluster,
            "cluster_length_utf16": next(e for e in ends if e > i.cluster)-i.cluster,
            "x_advance": p.x_advance, "y_advance": p.y_advance, "x_offset": p.x_offset, "y_offset": p.y_offset}
            for i,p in zip(buffer.glyph_infos,buffer.glyph_positions)]
        cases.append({"text": text, "liga": enabled, "kern": True, "language_tag": "TRK ", "glyphs": glyphs})
report = {"scope": "Explicit TRK is available in both tables; GSUB TRK has no liga while GPOS TRK retains kern. No missing-language fallback needed.",
    "font": str(path), "font_sha256": hashlib.sha256(data).hexdigest(), "cases": cases,
    "source_base_unchanged": source.read_bytes() == before,
    "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    "uharfbuzz_python_binding_version": hb.__version__, "harfbuzz_engine_version": hb.version_string()}
Path("/tmp/psdkit-font-shape-named-language-positive-oracle.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps({k: v for k,v in report.items() if k != "cases"}, ensure_ascii=False))
