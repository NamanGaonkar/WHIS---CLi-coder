# whis installer (Windows) - https://github.com/NamanGaonkar/WHIS---CLi-coder
# usage: iwr https://raw.githubusercontent.com/NamanGaonkar/WHIS---CLi-coder/main/install.ps1 | iex
$ErrorActionPreference = "Stop"
$repo = "NamanGaonkar/WHIS---CLi-coder"

# TLS 1.2 for older Windows PowerShell (5.1) - additive, keeps newer protocols
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# --- platform detection (must match release asset names) ---
$arch = $env:PROCESSOR_ARCHITECTURE
switch -Wildcard ($arch) {
    "AMD64" { $a = "amd64" }
    "ARM64" { $a = "arm64" }
    default { Write-Error "unsupported architecture '$arch'"; exit 1 }
}
$asset = "whis-windows-$a.exe"
$base = "https://github.com/$repo/releases/latest/download"
$tmp = Join-Path $env:TEMP ("whis-install-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

Write-Host "==> downloading whis (windows/$a, latest release)..."
try {
    Invoke-WebRequest -Uri "$base/$asset" -OutFile "$tmp\whis.exe" -UseBasicParsing
    Invoke-WebRequest -Uri "$base/$asset.sha256" -OutFile "$tmp\whis.exe.sha256" -UseBasicParsing
} catch {
    Write-Error "download failed: $_"
    exit 1
}

# --- verify checksum (tolerant of "hash  filename" format and CRLF) ---
$expected = (Get-Content "$tmp\whis.exe.sha256" -Raw).Trim() -split '\s+' | Select-Object -First 1
$actual = (Get-FileHash "$tmp\whis.exe" -Algorithm SHA256).Hash.ToLower()
if ($actual -ne $expected.ToLower()) {
    Write-Error "checksum mismatch (want $expected, got $actual) - download corrupted, aborting"
    exit 1
}
Write-Host "==> checksum ok"

# --- install: prefer a dir already on PATH, else the Go bin dir, else create one ---
$destDir = $null
foreach ($d in @("$env:USERPROFILE\go\bin", "$env:LOCALAPPDATA\Programs\whis")) {
    if (Test-Path $d) { $destDir = $d; break }
    if ($null -eq $destDir) { $destDir = $d }  # remember first candidate
}
if (-not (Test-Path $destDir)) { New-Item -ItemType Directory -Path $destDir -Force | Out-Null }
$dest = Join-Path $destDir "whis.exe"

# fail politely if whis is currently running (file lock)
if (Test-Path $dest) {
    try {
        $p = Get-Process whis -ErrorAction SilentlyContinue
        if ($p) { Write-Error "whis is currently running - close it and retry the install"; exit 1 }
    } catch { }
}

Move-Item -Force -Path "$tmp\whis.exe" -Destination $dest
Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue

# PATH check
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
$onPath = ($userPath -split ';') -contains $destDir -or ($machinePath -split ';') -contains $destDir
if (-not $onPath) {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$destDir", "User")
    Write-Host "==> added $destDir to your user PATH (new terminals only)"
    Write-Host "    for THIS window run: `$env:Path = `"$destDir;`$env:Path`""
}

$ver = & $dest -version 2>$null
Write-Host "==> installed: $dest $ver"
Write-Host "==> start: cd into a project and run: whis"
