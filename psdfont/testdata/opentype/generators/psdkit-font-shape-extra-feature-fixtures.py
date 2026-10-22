import copy
import hashlib
import json
from pathlib import Path

from fontTools.ttLib import TTFont
from fontTools.ttLib.tables import otTables
import uharfbuzz as hb

root = Path("/tmp/psdkit-font-shape-extra-features")
if root.exists():
    raise ValueError("Extra feature experiment already exists")
root.mkdir()
source = Path("/tmp/psdkit-font-shape-own-fixtures/base.ttf")
original = source.read_bytes()
font = TTFont(source)
font.recalcTimestamp = False
required = copy.deepcopy(font)
for record in required["GSUB"].table.ScriptList.ScriptRecord:
    record.Script.DefaultLangSys.ReqFeatureIndex = 0
    record.Script.DefaultLangSys.FeatureIndex = []
    record.Script.DefaultLangSys.FeatureCount = 0
language = copy.deepcopy(font)
script = next(r.Script for r in language["GSUB"].table.ScriptList.ScriptRecord if r.ScriptTag == "latn")
record = otTables.LangSysRecord(); record.LangSysTag = "TRK "; record.LangSys = otTables.LangSys()
record.LangSys.LookupOrder = None; record.LangSys.ReqFeatureIndex = 65535; record.LangSys.FeatureIndex = []; record.LangSys.FeatureCount = 0
script.LangSysRecord = [record]; script.LangSysCount = 1
rows = []
for name, variant, language_tag, hb_language in (("required-liga", required, "", "en"),
                                             ("named-language-without-liga", language, "TRK ", "tr")):
    path = root / (name + ".ttf"); variant.save(path, reorderTables=False)
    data = path.read_bytes(); font_hash = hashlib.sha256(data).hexdigest()
    face = hb.Face(data); selected = hb.Font(face); selected.scale = (1000, 1000); hb.ot_font_set_funcs(selected)
    cases = []
    for text in ("fi", "ffi", "fiAVAV"):
        for enabled in (False, True):
            buffer = hb.Buffer(); buffer.add_codepoints([ord(c) for c in text]); buffer.direction = "ltr"; buffer.script = "latn"; buffer.language = hb_language
            hb.shape(selected, buffer, {"liga": enabled, "kern": True, "ccmp": False, "locl": False, "rlig": False, "clig": False, "calt": False, "mark": False, "mkmk": False})
            cases.append({"text": text, "liga": enabled, "kern": True, "script": "latn", "language_tag": language_tag,
                "harfbuzz_language": hb_language, "glyph_ids": [i.codepoint for i in buffer.glyph_infos],
                "clusters": [i.cluster for i in buffer.glyph_infos], "positions": [[p.x_advance, p.y_advance, p.x_offset, p.y_offset] for p in buffer.glyph_positions]})
    rows.append({"variant": name, "path": str(path), "sha256": font_hash, "cases": cases})
report = {"scope": "Independent required-feature and explicit named-LangSys fixtures in this owned static TTF only; no Go implementation result.",
    "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "uharfbuzz_binding_version": hb.__version__, "harfbuzz_engine_version": hb.version_string(),
    "fonts": rows, "font_source_base_unchanged": source.read_bytes() == original}
Path("/tmp/psdkit-font-shape-extra-feature-oracle.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
for row in rows:
    print(row["variant"], row["sha256"], row["cases"])
