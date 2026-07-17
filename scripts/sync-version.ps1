# 从根目录 VERSION 同步到 package.json（唯一手改源：VERSION）
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$versionFile = Join-Path $root "VERSION"
if (-not (Test-Path $versionFile)) { throw "VERSION not found" }
$version = (Get-Content -LiteralPath $versionFile -Raw).Trim() -replace '^[vV]', ''
if ($version -eq "") { throw "VERSION empty" }

$pkgPath = Join-Path $root "frontend\package.json"
$pkg = Get-Content -LiteralPath $pkgPath -Raw
$updated = [regex]::Replace($pkg, '"version"\s*:\s*"[^"]*"', "`"version`": `"$version`"")
if ($updated -eq $pkg) {
  Write-Host "package.json already at $version"
} else {
  Set-Content -LiteralPath $pkgPath -Value $updated -NoNewline
  Write-Host "package.json -> $version"
}

$mainGo = Join-Path $root "backend\cmd\grok2api\main.go"
$main = Get-Content -LiteralPath $mainGo -Raw
$main2 = [regex]::Replace($main, '// @version\s+\S+', "// @version $version")
if ($main2 -ne $main) {
  Set-Content -LiteralPath $mainGo -Value $main2 -NoNewline
  Write-Host "main.go swagger @version -> $version"
}

foreach ($rel in @("backend\docs\docs.go", "backend\docs\swagger.yaml", "backend\docs\swagger.json")) {
  $path = Join-Path $root $rel
  if (-not (Test-Path $path)) { continue }
  $text = Get-Content -LiteralPath $path -Raw
  $next = $text
  if ($rel -like "*.go") {
    $next = [regex]::Replace($next, 'Version:\s+"[^"]*"', "Version:          `"$version`"")
  } elseif ($rel -like "*.yaml") {
    # 与 swag 一致：version: 3.0.0（无引号）
    $next = [regex]::Replace($next, '(?m)^version:\s*.*$', "version: $version")
  } elseif ($rel -like "*.json") {
    $next = [regex]::Replace($next, '"version":\s*"[^"]*"', "`"version`": `"$version`"", 1)
  }
  if ($next -ne $text) {
    Set-Content -LiteralPath $path -Value $next -NoNewline
    Write-Host "$rel -> $version"
  }
}

Write-Host "OK: app version = $version (source: VERSION)"
