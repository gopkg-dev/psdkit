import copy
import hashlib
import json
from pathlib import Path

from fontTools.ttLib import TTFont
from fontTools.ttLib.tables import otTables
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
import uharfbuzz as hb

source = Path("/tmp/psdkit-font-shape-own-fixtures/base.ttf")
before = source.read_bytes()
font = TTFont(source); font.recalcTimestamp = False
del font["GSUB"]; del font["GPOS"]
addOpenTypeFeaturesFromString(font, '''
languagesystem DFLT dflt;
languagesystem latn dflt;
lookup FIRST { sub f i by fi; } FIRST;
lookup SECOND { sub fi V by ffi; } SECOND;
feature liga { lookup SECOND; } liga;
feature zzzz { lookup FIRST; } zzzz;
lookup PAIR { pos A V -80; } PAIR;
feature kern { lookup PAIR; } kern;
feature zzzz { lookup PAIR; } zzzz;
''')
path = Path("/tmp/psdkit-font-shape-union-order.ttf")
if path.exists():
    raise ValueError("Union-order font already exists")
font.save(path, reorderTables=False)
data = path.read_bytes(); face = hb.Face(data); selected = hb.Font(face); selected.scale = (1000, 1000); hb.ot_font_set_funcs(selected)
cases = []
for text in ("fiV", "fiVAV", "AVAV", "fiAV"):
    for order in (("liga", "zzzz", "kern"), ("kern", "zzzz", "liga"), ("zzzz", "liga", "kern")):
        buffer = hb.Buffer(); buffer.add_codepoints([ord(c) for c in text]); buffer.direction = "ltr"; buffer.script = "latn"; buffer.language = "en"
        selection = {tag: True for tag in order}; selection.update({"ccmp": False, "locl": False, "rlig": False, "calt": False, "clig": False, "mark": False, "mkmk": False})
        hb.shape(selected, buffer, selection)
        ends = sorted(set(i.cluster for i in buffer.glyph_infos) | {len(text)})
        glyphs = [{"glyph_id": i.codepoint, "cluster_start_utf16": i.cluster,
            "cluster_length_utf16": next(e for e in ends if e > i.cluster)-i.cluster,
            "x_advance": p.x_advance, "y_advance": p.y_advance, "x_offset": p.x_offset, "y_offset": p.y_offset}
            for i,p in zip(buffer.glyph_infos,buffer.glyph_positions)]
        cases.append({"text": text, "liga": True, "kern": True,
            "feature_order": [{"Tag": tag, "Enabled": True} for tag in order], "glyphs": glyphs})
tables = {}
for tag in ("GSUB", "GPOS"):
    table = font[tag].table
    tables[tag] = {"features": [{"tag": r.FeatureTag, "lookup_indices": r.Feature.LookupListIndex} for r in table.FeatureList.FeatureRecord],
        "lookup_types": [l.LookupType for l in table.LookupList.Lookup]}
report = {"scope": "Independent owned GSUB union-order and duplicate GPOS lookup selection controls; request/tag order deliberately differs from lookup index order.",
    "font": str(path), "font_sha256": hashlib.sha256(data).hexdigest(), "selection_evidence": tables,
    "source_base_unchanged": source.read_bytes() == before, "cases": cases,
    "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    "uharfbuzz_python_binding_version": hb.__version__, "harfbuzz_engine_version": hb.version_string()}
Path("/tmp/psdkit-font-shape-union-order-oracle.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps(report, ensure_ascii=False))
