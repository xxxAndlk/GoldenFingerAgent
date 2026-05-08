import sys, traceback
try:
    import golden_finger
    print("golden_finger imported OK")
    print("Version:", golden_finger.__version__)
except Exception as e:
    print(f"Import error: {e}")
    traceback.print_exc()
