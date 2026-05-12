"""跨平台剪贴板操作。"""

import os


def copy_to_clipboard(text: str) -> bool:
    """将文本复制到系统剪贴板。成功返回 True。"""
    if not text.strip():
        return False
    if os.name == "nt":
        try:
            import subprocess
            subprocess.run(["clip"], input=text, text=True, check=True)
            return True
        except Exception:
            pass
    try:
        import tkinter as tk
        root = tk.Tk()
        root.withdraw()
        root.clipboard_clear()
        root.clipboard_append(text)
        root.update()
        root.destroy()
        return True
    except Exception:
        return False
