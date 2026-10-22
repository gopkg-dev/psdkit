import copy
import hashlib
import json
from pathlib import Path

from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
from fontTools.ttLib.tables import otTables
import uharfbuzz as hb

root = Path("/tmp/psdkit-font-shape-own-fixtures")
if root.exists():
    raise ValueError("Refusing to overwrite font fixture experiment")
root.mkdir()
names = [".notdef", "space", "A", "V", "T", "o", "a", "f", "i", "l", "ff", "fi", "fl", "ffi", "ffl", "eacute", "supplementary"]
widths = {name: 600 for name in names}
widths.update({"space": 250, "f": 310, "i": 180, "l": 190, "ff": 550, "fi": 430, "fl": 440, "ffi": 710, "ffl": 720, "supplementary": 650})
builder = FontBuilder(1000, isTTF=True)
builder.setupGlyphOrder(names)
builder.setupCharacterMap({32: "space", 65: "A", 86: "V", 84: "T", 111: "o", 97: "a", 102: "f", 105: "i", 108: "l", 233: "eacute", 0x1DF00: "supplementary"})
glyphs = {}
for name in names:
    pen = TTGlyphPen(None)
    if name != "space":
        pen.moveTo((20, 0)); pen.lineTo((widths[name] - 30, 0)); pen.lineTo((widths[name] - 30, 700)); pen.lineTo((20, 700)); pen.closePath()
    glyphs[name] = pen.glyph()
builder.setupGlyf(glyphs)
builder.setupHorizontalMetrics({name: (widths[name], 20) for name in names})
builder.setupHorizontalHeader(ascent=800, descent=-200)
builder.setupNameTable({"familyName": "psdkit Owned Shape Fixture", "styleName": "Regular", "uniqueFontIdentifier": "psdkit-owned-shape-static-v1",
    "fullName": "psdkit Owned Shape Fixture Regular", "psName": "psdkitOwnedShapeFixture-Regular", "version": "Version 1.000"})
builder.setupOS2(sTypoAscender=800, sTypoDescender=-200, usWinAscent=800, usWinDescent=200)
builder.setupPost()
builder.setupMaxp()
font = builder.font
font["head"].created = font["head"].modified = 2082844800
font.recalcTimestamp = False
addOpenTypeFeaturesFromString(font, '''
languagesystem DFLT dflt;
languagesystem latn dflt;
feature liga {
  sub f f i by ffi;
  sub f f l by ffl;
  sub f f by ff;
  sub f i by fi;
  sub f l by fl;
} liga;
@left = [f ffl];
@right = [i A];
feature kern {
  pos A V -80;
  pos V A -40;
  pos fi A <20 0 -15 0>;
  pos @left @right -25;
} kern;
''')
variants = {"base": font}
priority = copy.deepcopy(font)
for lookup in priority["GSUB"].table.LookupList.Lookup:
    for subtable in lookup.SubTable:
        for key in subtable.ligatures:
            subtable.ligatures[key].sort(key=lambda ligature: len(ligature.Component))
variants["short-ligature-priority"] = priority
extension = copy.deepcopy(font)
for tag, extension_type, cls in (("GSUB", 7, otTables.ExtensionSubst), ("GPOS", 9, otTables.ExtensionPos)):
    for lookup in extension[tag].table.LookupList.Lookup:
        original_type = lookup.LookupType
        subtables = []
        for subtable in lookup.SubTable:
            wrapped = cls(); wrapped.Format = 1; wrapped.ExtensionLookupType = original_type; wrapped.ExtSubTable = subtable
            subtables.append(wrapped)
        lookup.LookupType = extension_type; lookup.SubTable = subtables; lookup.SubTableCount = len(subtables)
variants["extension-equivalent"] = extension
flags = copy.deepcopy(font)
flags["GSUB"].table.LookupList.Lookup[0].LookupFlag = 8
variants["selected-ignore-marks-flag"] = flags
value2 = copy.deepcopy(font)
pair = value2["GPOS"].table.LookupList.Lookup[0].SubTable[0]
pair.ValueFormat2 = 1
for pair_set in pair.PairSet:
    for record in pair_set.PairValueRecord:
        record.Value2 = otTables.ValueRecord(); record.Value2.XPlacement = 0
variants["pair-second-value-present-zero"] = value2
single = copy.deepcopy(font)
subtable = otTables.SingleSubst(); subtable.mapping = {"A": "V"}
single["GSUB"].table.LookupList.Lookup[0].LookupType = 1
single["GSUB"].table.LookupList.Lookup[0].SubTable = [subtable]
single["GSUB"].table.LookupList.Lookup[0].SubTableCount = 1
variants["selected-single-substitution"] = single
reports = []
for name, variant in variants.items():
    path = root / (name + ".ttf")
    variant.save(path, reorderTables=False)
    data = path.read_bytes(); before = hashlib.sha256(data).hexdigest()
    face = hb.Face(data); selected_font = hb.Font(face); selected_font.scale = (1000, 1000); hb.ot_font_set_funcs(selected_font)
    order = variant.getGlyphOrder()
    cases = []
    for text in ("fi", "ffi", "ffl", "fiAVAVA", "AVAV", "A\U0001df00fiV", "fififl", "fA"):
        codepoints = [ord(c) for c in text]; utf16 = [0]
        for c in codepoints:
            utf16.append(utf16[-1] + (2 if c > 65535 else 1))
        for liga in (False, True):
            for kern in (False, True):
                buffer = hb.Buffer(); buffer.add_codepoints(codepoints)
                buffer.direction, buffer.script, buffer.language = "ltr", "latn", "en"
                buffer.cluster_level = hb.BufferClusterLevel.MONOTONE_CHARACTERS
                features = {"liga": liga, "kern": kern, "ccmp": False, "clig": False, "calt": False,
                    "rlig": False, "locl": False, "mark": False, "mkmk": False}
                hb.shape(selected_font, buffer, features)
                ends = sorted(set(info.cluster for info in buffer.glyph_infos) | {len(codepoints)})
                shaped = []
                for info, position in zip(buffer.glyph_infos, buffer.glyph_positions):
                    end = next(c for c in ends if c > info.cluster)
                    shaped.append({"glyph_id": info.codepoint, "glyph_name": order[info.codepoint],
                        "cluster_start_utf16": utf16[info.cluster], "cluster_length_utf16": utf16[end]-utf16[info.cluster],
                        "x_advance": position.x_advance, "y_advance": position.y_advance,
                        "x_offset": position.x_offset, "y_offset": position.y_offset})
                cases.append({"text": text, "liga": liga, "kern": kern, "utf16_units": utf16[-1], "glyphs": shaped})
    tables = {}
    for tag in ("GSUB", "GPOS"):
        table = variant[tag].table
        tables[tag] = {"lookups": [{"index": i, "type": lookup.LookupType, "flags": lookup.LookupFlag,
            "subtable_formats": [getattr(s, "Format", 1 if lookup.LookupType in {1, 4} else None) for s in lookup.SubTable],
            "extension_types": [getattr(s, "ExtensionLookupType", None) for s in lookup.SubTable],
            "value_formats": [[getattr(s, "ValueFormat1", None), getattr(s, "ValueFormat2", None)] for s in lookup.SubTable]}
            for i, lookup in enumerate(table.LookupList.Lookup)]}
    reports.append({"variant": name, "path": str(path), "sha256": before, "postscript_name": variant["name"].getDebugName(6),
        "units_per_em": 1000, "static_true_type": True, "features": tables, "cases": cases,
        "source_unchanged": before == hashlib.sha256(path.read_bytes()).hexdigest()})
report = {"scope": "Owned independently generated static TrueType OpenType fixtures; design-unit HarfBuzz oracle only; no Go shaping claim.",
    "fonttools_generation_script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    "harfbuzz_python_version": hb.__version__, "fonts": reports, "font_count": len(reports), "case_count": sum(len(r["cases"]) for r in reports),
    "source_unchanged": all(r["source_unchanged"] for r in reports),
    "expected_candidate_behavior": {"base": "Exact selected liga/kern glyphs/metrics/clusters", "short-ligature-priority": "Use actual short-first font order, not longest-first",
        "extension-equivalent": "Exact if GSUB7/GPOS9 supported; otherwise explicit selected-extension Unsupported",
        "selected-ignore-marks-flag": "Explicit selected-flag Unsupported is valid for zero-flag first scope; never silently assume ignored glyph classes",
        "pair-second-value-present-zero": "Second ValueRecord present even zero makes applied pair consume two positions", "selected-single-substitution": "Exact if GSUB1 supported; otherwise explicit selected-type Unsupported"}}
destination = Path("/tmp/psdkit-font-shape-owned-harfbuzz-oracle.json")
destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(json.dumps({key: report[key] for key in ("font_count", "case_count", "source_unchanged")}, ensure_ascii=False))
for row in reports:
    print(row["variant"], row["sha256"], json.dumps(row["features"]))
