from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile

root = Path(__file__).resolve().parent.parent
source = root / "dist" / "GoldenFinger-activity-v12"
output = root / "dist" / "GoldenFinger-activity-v12.zip"
with ZipFile(output, "w", ZIP_DEFLATED, compresslevel=6) as archive:
    for file in source.rglob("*"):
        if file.is_file() and file.name != "default_app.asar":
            archive.write(file, file.relative_to(source))
with ZipFile(output) as archive:
    error = archive.testzip()
    if error:
        raise RuntimeError(f"压缩包校验失败：{error}")
    if "GoldenFinger.exe" not in archive.namelist():
        raise RuntimeError("压缩包缺少 GoldenFinger.exe")
print(f"DESKTOP_ZIP_OK {output} {output.stat().st_size} bytes")
