$since = (Get-Date).AddDays(-2)
Get-ChildItem -Path 'D:\WebData\github\GoldenFingerAgent' -Recurse -Filter settings.json -File -ErrorAction SilentlyContinue |
  Where-Object { $_.LastWriteTime -gt $since } |
  Sort-Object LastWriteTime -Descending |
  Select-Object LastWriteTime, Length, FullName |
  Format-Table -AutoSize | Out-String -Width 250
