import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
for name, sql in connection.execute("SELECT name, sql FROM sqlite_master WHERE type='table' ORDER BY name"):
    print(f"\n[{name}]\n{sql}")
