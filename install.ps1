# WHIS installer - https://github.com/NamanGaonkar/WHIS---CLi-coder
# Run with: irm https://raw.githubusercontent.com/NamanGaonkar/WHIS---CLi-coder/main/install.ps1 | iex

$ErrorActionPreference = "Stop"
$Repo = "NamanGaonkar/WHIS---CLi-coder"

$banner = @"
  ██╗    ██╗ ██╗  ██╗ ██╗ ███████╗
  ██║    ██║ ██║  ██║ ██║ ██╔════╝
  ██║ █╗ ██║ ███████║ ██║ ███████╗
  ██║███╗██║ ██╔══██║ ██║ ╚════██║
  ╚███╔███╔╝ ██║  ██║ ██║ ███████║
   ╚══╝╚══╝  ╚═╝  ╚═╝ ╚═╝ ╚══════╝
"@
Write-Host $banner -ForegroundColor DarkYellow
Write-Host "  Installing WHIS [Personal Edition]..." -ForegroundColor Yellow
Write-Host ""

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "x64" }
$Target = "whis-windows-$Arch"
$Dest = "$env:USERPROFILE\.whis\bin"

# Resolve latest release version
$Version = "latest"
if ($env:VERSION) { $Version = $env:VERSION }
else {
  try {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
    $Version = $rel.tag_name
  } catch {
    Write-Host "  Failed to resolve latest release: $_" -ForegroundColor Red
    exit 1
  }
}
Write-Host "  Version: $Version"

$Url = "https://github.com/$Repo/releases/download/$Version/$Target.zip"
$Tmp = Join-Path $env:TEMP ("whis-install-" + [guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null

Write-Host "  Downloading $Target..."
$Zip = Join-Path $Tmp "whis.zip"
Invoke-WebRequest -Uri $Url -OutFile $Zip -UseBasicParsing

# Verify checksum if available
try {
  Invoke-WebRequest -Uri "$Url.sha256" -OutFile "$Zip.sha256" -UseBasicParsing
  $expected = (Get-Content "$Zip.sha256" | Out-String).Trim().Split(" ")[0]
  $actual = (Get-FileHash -Path $Zip -Algorithm SHA256).Hash.ToLower()
  if ($expected -ne $actual) { Write-Host "  Checksum MISMATCH" -ForegroundColor Red; exit 1 }
  Write-Host "  Checksum OK" -ForegroundColor Green
} catch { Write-Host "  (no checksum published, skipping verification)" -ForegroundColor DarkGray }

New-Item -ItemType Directory -Path $Dest -Force | Out-Null
Expand-Archive -Path $Zip -DestinationPath $Tmp -Force
$exe = Get-ChildItem -Path $Tmp -Recurse -Filter "whis.exe" | Select-Object -First 1
Copy-Item $exe.FullName (Join-Path $Dest "whis.exe") -Force

Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "  Installed to $Dest\whis.exe" -ForegroundColor Green
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$Dest*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$Dest", "User")
  Write-Host "  Added $Dest to your PATH (reopen your terminal to use it)"
}
Write-Host ""
Write-Host "  Run:  whis" -ForegroundColor Yellow
Write-Host ""
