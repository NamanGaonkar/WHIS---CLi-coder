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
Write-Host "  Installing WHIS (no edition tagging)" -ForegroundColor Yellow
Write-Host ""

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "x64" }
$Target = "whis-windows-$Arch"
$Dest = "$env:USERPROFILE\.whis\bin"

# Replace the binary at $TargetPath with $SourcePath.
#
# Windows locks the image file of a RUNNING process, so Copy-Item -Force onto
# a live whis.exe fails with "used by another process". The old approach staged
# a `whis-new.exe` plus a detached `for /L ... timeout /t 1` retry loop, but
# `timeout` cannot sleep in a hidden window (it has no console), so the whole
# 60-iteration budget elapsed in ~5s and the swap was silently abandoned if the
# user had not already exited. That produced the classic "it says it upgraded
# but whis --version is unchanged" report.
#
# Windows DOES permit renaming a running executable. So rename the live binary
# out of the way (which frees the path immediately), drop the new binary in,
# and let a detached helper delete the renamed leftover once the process exits.
function Install-WhisBinary {
  param([string]$SourcePath, [string]$TargetPath)

  $targetDir = Split-Path $TargetPath -Parent
  if (-not (Test-Path $targetDir)) { New-Item -ItemType Directory -Path $targetDir -Force | Out-Null }

  # Fast path: nothing is holding the file.
  try {
    Copy-Item $SourcePath $TargetPath -Force -ErrorAction Stop
    return $true
  } catch { }

  # Slow path: the target is running. Rename it aside - this is permitted even
  # while the process is alive, and it frees $TargetPath for the new binary.
  $stale = "$TargetPath.old"
  try { Remove-Item $stale -Force -ErrorAction SilentlyContinue } catch { }
  try {
    Rename-Item -LiteralPath $TargetPath -NewName ([IO.Path]::GetFileName($stale)) -ErrorAction Stop
  } catch {
    # Could not rename either (antivirus/permissions). Fall back to staging so
    # the next launch can pick it up.
    $staged = "$TargetPath.new"
    Copy-Item $SourcePath $staged -Force
    return $false
  }

  # CRITICAL: the target path is now free but the old binary sits in $stale.
  # If this copy fails we MUST put the old binary back, otherwise the user is
  # left with no whis at all. Never let an exception escape mid-swap.
  try {
    Copy-Item $SourcePath $TargetPath -Force -ErrorAction Stop
  } catch {
    try {
      Rename-Item -LiteralPath $stale -NewName ([IO.Path]::GetFileName($TargetPath)) -ErrorAction Stop
    } catch {
      # Rollback failed too. Point the user straight at the surviving file.
      Write-Host "  CRITICAL: could not install or restore $TargetPath" -ForegroundColor Red
      Write-Host "  Your previous binary is at: $stale" -ForegroundColor Red
      Write-Host "  Copy it back to: $TargetPath" -ForegroundColor Red
    }
    return $false
  }

  # The renamed original is still mapped by the live process, so it usually
  # cannot be deleted yet. Try anyway, and if it is still locked just leave it:
  # Remove-WhisStaleFiles sweeps these leftovers at the start of every install,
  # once the old process has exited. That is deterministic, unlike a detached
  # retry loop whose sleep silently fails in a window without a console.
  try { Remove-Item $stale -Force -ErrorAction SilentlyContinue } catch { }
  return $true
}

# Delete whis.exe.old / whis.exe.new leftovers from earlier upgrades. These can
# only be removed once the process that had them mapped has exited, so this runs
# on each install rather than from a background retry loop.
#
# SAFETY: if no real whis.exe is present, a .old file is the user's ONLY working
# binary (an interrupted swap). Never delete it in that case - restore it instead.
function Remove-WhisStaleFiles {
  param([string]$Dir)
  if (-not (Test-Path $Dir)) { return }
  $live = Join-Path $Dir "whis.exe"
  $hasLive = Test-Path $live
  if (-not $hasLive) {
    $orphan = Get-ChildItem -Path $Dir -Filter "whis*.old" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($orphan) {
      try {
        Rename-Item -LiteralPath $orphan.FullName -NewName "whis.exe" -ErrorAction Stop
        Write-Host "  Restored $live from a previous interrupted upgrade" -ForegroundColor Yellow
        $hasLive = $true
      } catch { }
    }
  }
  if (-not $hasLive) { return }
  Get-ChildItem -Path $Dir -Filter "whis*.old" -ErrorAction SilentlyContinue | ForEach-Object {
    try { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue } catch { }
  }
  Get-ChildItem -Path $Dir -Filter "whis*.new" -ErrorAction SilentlyContinue | ForEach-Object {
    try { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue } catch { }
  }
}

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
# Clear leftovers from a previous upgrade before writing the new binary.
Remove-WhisStaleFiles -Dir $Dest
Expand-Archive -Path $Zip -DestinationPath $Tmp -Force
$exe = Get-ChildItem -Path $Tmp -Recurse -Filter "whis.exe" | Select-Object -First 1
$Installed = Install-WhisBinary -SourcePath $exe.FullName -TargetPath (Join-Path $Dest "whis.exe")
if (-not $Installed) {
  Write-Host "  whis is running and could not be replaced - update staged, run again after closing whis" -ForegroundColor Yellow
}

Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue

# Also update the original exe's folder (e.g. ~\go\bin) so a new terminal
# always picks up the latest whis instead of a shadowed stale copy.
if ($LazyOriginalExe -and (Test-Path $LazyOriginalExe)) {
  $OriginalDir = Split-Path $LazyOriginalExe -Parent
  $Canonical = Join-Path $Dest "whis.exe"
  if ((Resolve-Path $LazyOriginalExe -ErrorAction SilentlyContinue).Path -ne $Canonical) {
    if (Install-WhisBinary -SourcePath $Canonical -TargetPath (Join-Path $OriginalDir "whis.exe")) {
      Write-Host "  Also updated $OriginalDir\whis.exe" -ForegroundColor DarkGray
    } else {
      Write-Host "  Could not update $OriginalDir\whis.exe (it is running) - close whis and re-run" -ForegroundColor Yellow
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
  if (Install-WhisBinary -SourcePath (Join-Path $Dest "whis.exe") -TargetPath $old) {
    Write-Host "  Also updated $old" -ForegroundColor DarkGray
  } else {
    Write-Host "  Could not update $old (it is running) - close whis and re-run" -ForegroundColor Yellow
  }
}

# Prove the install actually took, so a silently-failed swap can never be
# reported as success again.
$InstalledVersion = $null
try { $InstalledVersion = (& (Join-Path $Dest "whis.exe") --version 2>$null | Select-Object -First 1) } catch { }
$ExpectedVersion = $Version -replace '^v', ''
Write-Host ""
if (-not $InstalledVersion) {
  Write-Host "  Could not verify (whis is still running) - check 'whis --version' after closing whis" -ForegroundColor Yellow
} elseif ($InstalledVersion.Trim() -ne $ExpectedVersion) {
  Write-Host "  WARNING: installed binary reports $InstalledVersion but $ExpectedVersion was requested." -ForegroundColor Red
  Write-Host "  Close whis and re-run the installer to finish the upgrade." -ForegroundColor Yellow
} else {
  Write-Host "  Verified: whis --version reports $InstalledVersion" -ForegroundColor Green
}
Write-Host ""
Write-Host "  Run:  whis" -ForegroundColor Yellow
Write-Host ""
