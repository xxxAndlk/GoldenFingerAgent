"""从用户提供的截图中提取深色圆形图标，重建为透明背景的高分辨率源图。

输入：任务栏截图（深色圆形图标 + 下方一条分隔短线）
输出：desktop/scripts/app-icon-source.png（512x512，透明背景的圆形图标）
用法：python desktop/scripts/extract-icon-source.py <截图路径>
"""

import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFilter

OUT = Path(__file__).resolve().parent / "app-icon-source.png"
SIZE = 512


def detect_circle(dark):
    """找出深色圆形的包围盒（忽略图标下方那条分隔短线）。"""
    rows = [y for y in range(dark.shape[0]) if dark[y].sum() >= 6]
    top, bottom = rows[0], rows[-1]
    while bottom - top > 8 and dark[bottom].sum() < 10:
        bottom -= 1
    band = dark[top : bottom + 1]
    cols = np.where(band.any(axis=0))[0]
    return int(cols.min()), int(top), int(cols.max()) + 1, int(bottom) + 1


def main():
    if len(sys.argv) < 2:
        raise SystemExit("用法：python extract-icon-source.py <截图路径>")
    source = Image.open(sys.argv[1]).convert("RGB")
    rgb = np.asarray(source).astype(np.float64)
    lum = rgb.mean(axis=2)
    left, top, right, bottom = detect_circle(lum < 140)
    crop = source.crop((left, top, right, bottom))
    side = max(crop.size)
    square = Image.new("RGB", (side, side), crop.getpixel((0, 0)))
    square.paste(crop, ((side - crop.width) // 2, (side - crop.height) // 2))

    arr = np.asarray(square).astype(np.float64)
    lum = arr.mean(axis=2)
    ys, xs = np.mgrid[0:side, 0:side]
    inside = ((xs - (side - 1) / 2) ** 2 + (ys - (side - 1) / 2) ** 2) <= (side / 2 * 0.80) ** 2

    dark_level = float(np.percentile(lum[inside], 15))
    bright = inside & (lum > dark_level + 18)
    glyph_level = float(np.percentile(lum[bright], 85))
    core = bright & (lum > (dark_level + glyph_level) / 2)
    glyph_color = arr[core].mean(axis=0)
    base_color = arr[inside & (lum <= dark_level + 12)].mean(axis=0)

    # 字形亮度遮罩：深色圆面为底，亮起的原子图案按比例叠上去。
    alpha_glyph = np.clip((lum - dark_level) / max(glyph_level - dark_level, 1.0), 0.0, 1.0)

    rgb_out = base_color[None, None, :] * (1 - alpha_glyph[:, :, None]) + glyph_color[None, None, :] * alpha_glyph[:, :, None]

    big = Image.fromarray(np.clip(rgb_out, 0, 255).astype(np.uint8), "RGB").resize((SIZE, SIZE), Image.LANCZOS)

    mask = Image.new("L", (SIZE, SIZE), 0)
    ImageDraw.Draw(mask).ellipse((0, 0, SIZE - 1, SIZE - 1), fill=255)
    mask = mask.filter(ImageFilter.GaussianBlur(0.7))

    out = big.convert("RGBA")
    out.putalpha(mask)
    out.save(OUT)
    print(f"{OUT} bbox=({left},{top})-({right},{bottom}) dark={dark_level:.0f} glyph={glyph_level:.0f} base={base_color.round()} glyph_rgb={glyph_color.round()}")


if __name__ == "__main__":
    main()
