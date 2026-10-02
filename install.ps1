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
# The executable that started this install (the one the user ran).
$LazyOriginalExe = if ($env:WhisOriginalExe) { $env:WhisOriginalExe } else { $null }

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
try {
  Copy-Item $exe.FullName (Join-Path $Dest "whis.exe") -Force
} catch {
  # WHIS is running - Windows locks the exe of a live process. Stage it and
  # swap via a detached helper that retries for 60s after this exits.
  Write-Host "  whis is running - staging update to finish when you exit whis..." -ForegroundColor Yellow
  Copy-Item $exe.FullName (Join-Path $Dest "whis-new.exe") -Force
  $swap = 'for /L %i in (1,1,60) do (move /y "' + $Dest + '\whis-new.exe" "' + $Dest + '\whis.exe" >nul 2>&1 & timeout /t 1 /nobreak >nul)'
  Start-Process -FilePath "cmd.exe" -ArgumentList "/c", $swap -WindowStyle Hidden
}

Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue

# Also update the original exe's folder (e.g. ~\go\bin) so a new terminal
# always picks up the latest whis instead of a shadowed stale copy.
if ($LazyOriginalExe -and (Test-Path $LazyOriginalExe)) {
  $OriginalDir = Split-Path $LazyOriginalExe -Parent
  if ((Resolve-Path $LazyOriginalExe -ErrorAction SilentlyContinue).Path -ne $Dest) {
    try {
      Copy-Item "$Dest\\whis.exe" "$OriginalDir\\whis.exe" -Force
      Write-Host "  Also updated $OriginalDir\\whis.exe" -ForegroundColor DarkGray
    } catch {
      Copy-Item "$Dest\\whis-new.exe" "$OriginalDir\\whis-new.exe" -Force
      $swap = 'for /L %i in (1,1,60) do (move /y `"' + $OriginalDir + '\whis-new.exe" `"' + $OriginalDir + '\whis.exe" >nul 2>&1 & timeout /t 1 /nobreak >nul)'
      Start-Process -FilePath "cmd.exe" -ArgumentList "/c", $swap -WindowStyle Hidden
    }
  }
}

Write-Host ""
Write-Host "  Installed to $Dest\\whis.exe" -ForegroundColor Green
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$Dest*") {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$Dest", "User")
  Write-Host "  Added $Dest to your PATH (reopen your terminal to use it)"
}
# If old WHIS copies live elsewhere on PATH (e.g. ~\go\bin), update them too
# so the freshly installed version isn't shadowed by a stale one.
$oldSpots = @()
try { $oldSpots += (where.exe whis 2>$null | Where-Object { $_ -and ((Resolve-Path $_ -ErrorAction SilentlyContinue).Path -ne (Join-Path $Dest "whis.exe")) }) } catch {}
$oldSpots = $oldSpots | Select-Object -Unique
foreach ($old in $oldSpots) {
  try {
    Copy-Item (Join-Path $Dest "whis.exe") $old -Force
  } catch {
    Copy-Item (Join-Path $Dest "whis.exe") "$old.new" -Force
    Start-Process -FilePath "cmd.exe" -ArgumentList "/c", "for /L %i in (1,1,30) do (move /y `"$old.new`" `"$old`" >nul 2>&1 & timeout /t 2 /nobreak >nul)" -WindowStyle Hidden
  }
  Write-Host "  Also updated $old" -ForegroundColor DarkGray
}
Write-Host ""
Write-Host "  Run:  whis" -ForegroundColor Yellow
Write-Host ""
