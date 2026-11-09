"""生成金手指管家的应用图标与托盘图标。

母版优先取 Electron 自带 default_app.asar 里的 1024px 原图（清晰）；
取不到时回退到 desktop/scripts/app-icon-source.png（可能来自截图重建，会糊）。
生成：
  - app-icon.png / app-icon.ico   窗口标题栏、任务栏、快捷方式
  - tray-icon.png / tray-icon.ico 系统托盘空闲态
  - tray-icon-busy.*              系统托盘执行态（右下角状态点）
  - app-icon.css                  悬浮球位图（与任务栏/托盘同源）
每个尺寸单独缩放，16/20/24px 的小图标也保持可辨认。
用法：python desktop/scripts/create-tray-icon.py
"""

import base64
import io
import json
import struct
from pathlib import Path

from PIL import Image, ImageDraw

OUT = Path(__file__).resolve().parent.parent
SOURCE = Path(__file__).resolve().parent / "app-icon-source.png"
ASAR = OUT / "node_modules" / "electron" / "dist" / "resources" / "default_app.asar"
ICO_SIZES = (16, 20, 24, 32, 40, 48, 64, 128, 256)
ANDROID_DENSITIES = {"mdpi": 108, "hdpi": 162, "xhdpi": 216, "xxhdpi": 324, "xxxhdpi": 432}
BUSY_DOT = (48, 209, 88)


def read_asar(asar_path, name):
    """从 asar 包里取出单个文件内容（头部是 JSON，文件数据紧随其后）。"""
    data = asar_path.read_bytes()
    header_size = struct.unpack_from("<I", data, 12)[0]
    header = json.loads(data[16 : 16 + header_size].decode("utf-8"))
    entry = header["files"][name]
    start = 16 + header_size + int(entry["offset"])
    return data[start : start + entry["size"]]


def load_master_from_asar():
    """官方 1024px 原图：矢量渲染的清晰母版，比截图重建的源图锐利得多。"""
    if not ASAR.exists():
        return None
    try:
        return Image.open(io.BytesIO(read_asar(ASAR, "icon.png"))).convert("RGBA")
    except Exception:
        return None


def load_master(preloaded=None):
    """载入母版：按实际内容裁成正方形，保留透明背景。"""
    image = preloaded if preloaded is not None else load_master_from_asar()
    if image is None:
        image = Image.open(SOURCE).convert("RGBA")
    bbox = image.getbbox()
    if bbox:
        image = image.crop(bbox)
    side = max(image.size)
    square = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    square.paste(image, ((side - image.width) // 2, (side - image.height) // 2))
    return square


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


def write_web_icon(master):
    """手机端 H5 用的应用图标：网页 favicon 与页面内的管家头像。"""
    web = OUT.parent / "web"
    icon = master.resize((128, 128), Image.LANCZOS)
    icon.save(web / "app-icon.png")
    return web / "app-icon.png"


def write_android_foreground(master):
    """Android 自适应图标前景：108dp 画布上放 66dp 的圆形，留出系统裁切安全区。"""
    res = OUT.parent / "android" / "app" / "src" / "main" / "res"
    written = []
    for density, canvas in ANDROID_DENSITIES.items():
        size = round(canvas * 66 / 108)
        layer = Image.new("RGBA", (canvas, canvas), (0, 0, 0, 0))
        layer.alpha_composite(
            master.resize((size, size), Image.LANCZOS),
            ((canvas - size) // 2, (canvas - size) // 2),
        )
        folder = res / f"drawable-{density}"
        folder.mkdir(parents=True, exist_ok=True)
        target = folder / "ic_launcher_atom.png"
        layer.save(target)
        written.append(target)
        # 还要留一份默认密度：只有密度限定而没有默认版本时，
        # AAPT/AGP 资源合并会报找不到 @drawable 引用。
        if density == "mdpi":
            default = res / "drawable" / "ic_launcher_atom.png"
            default.parent.mkdir(parents=True, exist_ok=True)
            layer.save(default)
            written.append(default)
    return written


def main():
    official = load_master_from_asar()
    if official is not None:
        # 仓库里留一份官方母版，没有 node_modules 时仍能复现同一张图。
        official.save(SOURCE)
    master = load_master(official)
    master.resize((512, 512), Image.LANCZOS).save(OUT / "app-icon.png")
    write_ico(OUT / "app-icon.ico", [render(master, size) for size in ICO_SIZES])
    write_orb_css(render(master, 192))
    write_web_icon(master)
    write_android_foreground(master)

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
    artifacts = OUT / "artifacts"
    artifacts.mkdir(parents=True, exist_ok=True)
    preview.save(artifacts / "icon-preview.png")
    print(OUT / "app-icon.ico")
    print(OUT / "tray-icon.ico")


if __name__ == "__main__":
    main()
