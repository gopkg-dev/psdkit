"""Regenerate owned synthetic TIFF fixtures with Pillow and independent LibTIFF."""
from pathlib import Path
import subprocess
import tempfile

from PIL import Image

root = Path(__file__).resolve().parent
with tempfile.TemporaryDirectory(prefix="psdkit-lzw-") as temporary:
    for name, mode, width, height, rows, predictor, endian in [
        ("rgb-lzw.tif", "RGB", 257, 20, 20, 1, "-L"),
        ("rgba-lzw-predictor.tif", "RGBA", 257, 20, 7, 2, "-B"),
        ("rgba-lzw-small.tif", "RGBA", 2, 2, 1, 2, "-L"),
        ("rgba-lzw-small-updated.tif", "RGBA", 2, 2, 1, 2, "-B"),
    ]:
        state = 0x12345678 if "updated" in name else 0x9E3779B9
        raw = bytearray()
        for _ in range(width * height * len(mode)):
            state = (1664525 * state + 1013904223) & 0xFFFFFFFF
            raw.append(state >> 24)
        image = Image.frombytes(mode, (width, height), bytes(raw))
        source = Path(temporary) / "raw.tif"
        image.save(source, compression="raw")
        destination = root / name
        subprocess.run([
            "tiffcp", "-c", f"lzw:{predictor}", "-r", str(rows), endian,
            str(source), str(destination),
        ], check=True)
        decoded = Image.open(destination)
        assert decoded.tobytes() == bytes(raw)
        print(name, destination.stat().st_size, decoded.tag_v2[259], decoded.tag_v2[317])
