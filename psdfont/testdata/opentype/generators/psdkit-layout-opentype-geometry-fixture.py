#!/usr/bin/env python3
"""Owned static glyf fixture for independent path/composite/y-position QA."""
import hashlib
import json
from pathlib import Path

import fontTools
import uharfbuzz as hb
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.ttLib import TTFont

ROOT = Path("/tmp/psdkit-layout-opentype-geometry-fixture-v3")
SOURCE = Path("/tmp/psdkit-font-shape-portable-bundle-v2/fonts/base.ttf")
SOURCE_SHA = "d509ffa7479c63f5b0c60f05c4feafb751f4ea12a3efd4f7bc540684b144a8f2"


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    if sha(SOURCE) != SOURCE_SHA:
        raise ValueError("Frozen base font changed")
    ROOT.mkdir(exist_ok=False)
    (ROOT / "fonts").mkdir()
    font = TTFont(SOURCE, recalcTimestamp=False)
    glyf = font["glyf"]
    pen = TTGlyphPen(None)
    pen.moveTo((-50, -120))
    pen.qCurveTo((210, 960), (430, 830), (550, 320))
    pen.lineTo((470, -100))
    pen.qCurveTo((200, -240), (-50, -120))
    pen.closePath()
    glyf["A"] = pen.glyph()
    pen = TTGlyphPen({name: glyf[name] for name in font.getGlyphOrder()})
    pen.addComponent("A", (0.625, 0.125, -0.25, 0.75, 80, 120))
    glyf["V"] = pen.glyph()
    pen = TTGlyphPen({name: glyf[name] for name in font.getGlyphOrder()})
    pen.addComponent("A", (0.5, 0.0, 0.0, 0.625, 15, -30))
    pen.addComponent("i", (0.5, 0.125, -0.125, 0.75, 285, 45))
    glyf["fi"] = pen.glyph()
    pen = TTGlyphPen(None)
    pen.moveTo((30, 20))
    pen.qCurveTo((200, 900), (470, 760), (680, 0))
    pen.lineTo((540, -140))
    pen.qCurveTo((190, -230), (30, 20))
    pen.closePath()
    glyf["ffi"] = pen.glyph()
    pen = TTGlyphPen(None)
    pen.qCurveTo((50, 100), (300, 850), (620, 100), None)
    pen.closePath()
    glyf["ffl"] = pen.glyph()
    for name in ("A", "V", "fi", "ffi", "ffl"):
        glyf[name].recalcBounds(glyf)
        width, _ = font["hmtx"][name]
        font["hmtx"][name] = (width, glyf[name].xMin)
    font["name"].setName("psdkitOwnedLayoutGeometry-Regular", 6, 3, 1, 0x409)
    font["name"].setName("psdkit Owned Layout Geometry Regular", 4, 3, 1, 0x409)
    for table in ("GSUB", "GPOS"):
        if table in font:
            del font[table]
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
  pos A <17 43 -80 29> V <-11 -23 5 -7>;
  pos V <-9 -31 -40 -13> A <7 19 -3 11>;
  pos fi <20 51 -15 37> A <-13 -41 8 -17>;
  pos @left <0 -21 -25 -19> @right <0 0 0 0>;
} kern;
''')
    font.recalcTimestamp = False
    font_path = ROOT / "fonts/geometry-y-position.ttf"
    font.save(font_path, reorderTables=False)
    font.close()
    data = font_path.read_bytes()
    face = hb.Face(data)
    shaped_font = hb.Font(face)
    shaped_font.scale = (1000, 1000)
    hb.ot_font_set_funcs(shaped_font)
    cases = []
    texts = ("", " ", "AV", "AVA", "AVAVA", "VAV", "fiA", "fiAVA", "ffi", "ffl", "fflA",
             "A\U0001df00fiV", "fi AVA", "VAfiA")
    for text in texts:
        utf16 = [0]
        for c in text:
            utf16.append(utf16[-1] + (2 if ord(c) > 0xffff else 1))
        for liga in (False, True):
            for kern in (False, True):
                buffer = hb.Buffer()
                buffer.add_codepoints([ord(c) for c in text])
                buffer.direction, buffer.script, buffer.language = "ltr", "latn", "en"
                buffer.cluster_level = hb.BufferClusterLevel.MONOTONE_CHARACTERS
                feature_map = {"liga": liga, "kern": kern, "ccmp": False, "clig": False,
                               "calt": False, "rlig": False, "locl": False, "mark": False, "mkmk": False}
                hb.shape(shaped_font, buffer, feature_map)
                infos, positions = buffer.glyph_infos or [], buffer.glyph_positions or []
                starts = [utf16[info.cluster] for info in infos]
                ends = starts[1:] + [utf16[-1]] if starts else []
                glyphs = [{"glyph_id": info.codepoint, "cluster_start_utf16": start,
                           "cluster_length_utf16": end - start, "x_advance": pos.x_advance,
                           "y_advance": pos.y_advance, "x_offset": pos.x_offset, "y_offset": pos.y_offset}
                          for info, pos, start, end in zip(infos, positions, starts, ends)]
                cases.append({"id": "geometry:" + str(len(cases)), "font": "fonts/geometry-y-position.ttf",
                              "text": text, "script": "latn", "language": "",
                              "features": [{"tag": "liga", "enabled": liga}, {"tag": "kern", "enabled": kern}],
                              "expected_error": "missing_glyph" if any(g["glyph_id"] == 0 for g in glyphs) else "",
                              "utf16_units": utf16[-1], "glyphs": glyphs})
    shape = {"scope": "Owned QA fixture only: quadratic/off-curve/composite contours and GPOS x/y advance/placement; no Go implementation results",
             "fonts": [{"file": "fonts/geometry-y-position.ttf", "sha256": sha(font_path)}], "cases": cases,
             "fonttools_version": fontTools.__version__, "uharfbuzz_binding_version": hb.__version__,
             "harfbuzz_engine_version": hb.version_string(), "script_sha256": sha(Path(__file__)),
             "base_font_sha256": SOURCE_SHA, "base_font_unchanged": sha(SOURCE) == SOURCE_SHA}
    oracle_path = ROOT / "oracle.json"
    oracle_path.write_text(json.dumps(shape, ensure_ascii=True, indent=2) + "\n")
    manifest = {"scope": shape["scope"], "font_count": 1, "case_count": len(cases),
                "files": {"fonts/geometry-y-position.ttf": sha(font_path), "oracle.json": sha(oracle_path)},
                "all_fonts_self_generated": True}
    (ROOT / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({"root": str(ROOT), "font_sha256": sha(font_path), "oracle_sha256": sha(oracle_path),
                      "cases": len(cases), "base_unchanged": shape["base_font_unchanged"],
                      "nonzero_y_advance": sum(g["y_advance"] != 0 for c in cases for g in c["glyphs"]),
                      "nonzero_y_offset": sum(g["y_offset"] != 0 for c in cases for g in c["glyphs"])}))


if __name__ == "__main__":
    main()
