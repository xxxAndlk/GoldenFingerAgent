"""生成金手指管家的应用图标与托盘图标。

源图：desktop/scripts/app-icon-source.png（蓝底金色手指 + 房子）。
裁出圆角方形后生成：
  - app-icon.png / app-icon.ico   窗口标题栏、任务栏、快捷方式
  - tray-icon.png / tray-icon.ico 系统托盘空闲态
  - tray-icon-busy.*              系统托盘执行态（右下角状态点）
  - app-icon.css                  悬浮球位图（与任务栏/托盘同源）
每个尺寸单独缩放，16/20/24px 的小图标也保持可辨认。
用法：python desktop/scripts/create-tray-icon.py
"""

import base64
import io
import struct
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw

OUT = Path(__file__).resolve().parent.parent
SOURCE = Path(__file__).resolve().parent / "app-icon-source.png"
ICO_SIZES = (16, 20, 24, 32, 40, 48, 64, 128, 256)
CORNER_RATIO = 0.215
BUSY_DOT = (48, 209, 88)
SOURCE_THRESHOLD = 16


def load_master():
    """从源图裁出圆角方形，并去掉四周的纯黑底色。"""
    image = Image.open(SOURCE).convert("RGBA")
    rgb = np.asarray(image.convert("RGB"), dtype=np.int16)
    bright = rgb.max(axis=2)
    ys, xs = np.where(bright > SOURCE_THRESHOLD)
    left, top, right, bottom = xs.min(), ys.min(), xs.max() + 1, ys.max() + 1
    side = max(right - left, bottom - top)
    cx, cy = (left + right) // 2, (top + bottom) // 2
    left = max(0, min(cx - side // 2, image.width - side))
    top = max(0, min(cy - side // 2, image.height - side))
    crop = image.crop((left, top, left + side, top + side))

    big = side * 2
    mask = Image.new("L", (big, big), 0)
    ImageDraw.Draw(mask).rounded_rectangle(
        (0, 0, big - 1, big - 1), radius=round(big * CORNER_RATIO), fill=255
    )
    crop.putalpha(mask.resize((side, side), Image.LANCZOS))
    return crop


def render(master, size):
    return master.resize((size, size), Image.LANCZOS)


def add_busy_dot(image):
    size = image.size[0]
    draw = ImageDraw.Draw(image)
    radius = size * 0.15
    cx = size * 0.795
    cy = size * 0.795
    ring = max(1.0, size * 0.035)
    draw.ellipse(
        (cx - radius - ring, cy - radius - ring, cx + radius + ring, cy + radius + ring),
        fill=(255, 255, 255, 245),
    )
    draw.ellipse((cx - radius, cy - radius, cx + radius, cy + radius), fill=BUSY_DOT + (255,))
    return image


def write_ico(path, frames):
    """手写 ICO 容器：每个尺寸都用自己缩放好的位图。"""
    frames_data = []
    for frame in frames:
        size = frame.size[0]
        rgba = frame.convert("RGBA")
        rows = [
            rgba.crop((0, y, size, y + 1)).tobytes("raw", "BGRA")
            for y in range(size - 1, -1, -1)
        ]
        header = struct.pack(
            "<IiiHHIIiiII", 40, size, size * 2, 1, 32, 0, size * size * 4, 0, 0, 0, 0
        )
        frames_data.append(header + b"".join(rows) + b"\x00" * (((size + 31) // 32) * 4 * size))

    offset = 6 + 16 * len(frames)
    directory = b""
    for frame, data in zip(frames, frames_data):
        size = frame.size[0]
        directory += struct.pack(
            "<BBBBHHII",
            size if size < 256 else 0,
            size if size < 256 else 0,
            0,
            0,
            1,
            32,
            len(data),
            offset,
        )
        offset += len(data)
    path.write_bytes(struct.pack("<HHH", 0, 1, len(frames)) + directory + b"".join(frames_data))


def write_orb_css(icon):
    """把图标写进 CSS：悬浮球直接用位图，避免 CSP 拦外链图片，也保证同源。"""
    buffer = io.BytesIO()
    icon.save(buffer, format="PNG", optimize=True)
    encoded = base64.b64encode(buffer.getvalue()).decode("ascii")
    css = (
        "/* 由 desktop/scripts/create-tray-icon.py 生成，勿手改。 */\n"
        "/* 悬浮球使用与任务栏图标、托盘图标同一张图。 */\n"
        ".orb {\n"
        f'  background-image: url("data:image/png;base64,{encoded}");\n'
        "  background-size: 100% 100%;\n"
        "  background-repeat: no-repeat;\n"
        "}\n"
    )
    (OUT / "app-icon.css").write_text(css, encoding="utf-8")
    return OUT / "app-icon.css"


def main():
    master = load_master()
    master.resize((512, 512), Image.LANCZOS).save(OUT / "app-icon.png")
    write_ico(OUT / "app-icon.ico", [render(master, size) for size in ICO_SIZES])
    write_orb_css(render(master, 192))

    for name, busy in (("tray-icon", False), ("tray-icon-busy", True)):
        icon = render(master, 64)
        if busy:
            add_busy_dot(icon)
        icon.save(OUT / f"{name}.png")
        frames = []
        for size in ICO_SIZES:
            frame = render(master, size)
            frames.append(add_busy_dot(frame) if busy else frame)
        write_ico(OUT / f"{name}.ico", frames)

    preview = Image.new("RGBA", (32 * 9 + 8 * 8, 32), (245, 245, 247, 255))
    x = 0
    for size in ICO_SIZES:
        if size > 32:
            continue
        preview.alpha_composite(render(master, size).resize((32, 32), Image.NEAREST), (x, 0))
        x += 40
    preview.save(OUT / "scripts" / "icon-preview.png")
    print(OUT / "app-icon.ico")
    print(OUT / "tray-icon.ico")


if __name__ == "__main__":
    main()
