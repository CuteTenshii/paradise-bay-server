$ErrorActionPreference = "Stop"
Set-Location -LiteralPath $PSScriptRoot

Write-Host "=== Paradise Bay Installer ===" -ForegroundColor Cyan
Write-Host ""

# ---------------------------------------------------------------------------
# Step 1: Extract the .appx package to AppxPackageExtracted\.
#         If already extracted, skip. If the .appx is missing, tell the user
#         where to download it.
# ---------------------------------------------------------------------------
Write-Host "[1/4] Checking for extracted package ..." -ForegroundColor Yellow

$appxName    = "king.com.ParadiseBay_3.9.0.0_x86__kgqvnymyfvs32.appx"
$appxPath    = Join-Path $PSScriptRoot $appxName
$extractDest = Join-Path $PSScriptRoot "AppxPackageExtracted"

if (Test-Path -LiteralPath (Join-Path $extractDest "AppxManifest.xml")) {
    Write-Host "  Already extracted - skipping." -ForegroundColor Gray
} elseif (Test-Path -LiteralPath $appxPath) {
    Write-Host "  Extracting $appxName (this may take several minutes) ..." -ForegroundColor Yellow
    if (Test-Path -LiteralPath $extractDest) {
        Remove-Item -LiteralPath $extractDest -Recurse -Force
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [System.IO.Compression.ZipFile]::ExtractToDirectory($appxPath, $extractDest)
    Write-Host "  Extracted to AppxPackageExtracted" -ForegroundColor Green
} else {
    Write-Host ""
    Write-Host "ERROR: $appxName not found!" -ForegroundColor Red
    Write-Host ""
    Write-Host "  Download it from: https://store.rg-adguard.net/" -ForegroundColor Yellow
    Write-Host "  Filter by 'ProductId', search for: 9nblggh5l706" -ForegroundColor Yellow
    Write-Host "  Download 'king.com.ParadiseBay_3.9.0.0_x86__kgqvnymyfvs32.appx' and place it in the same folder as this one." -ForegroundColor Yellow
    Write-Host ""
    exit 1
}

Set-Location -LiteralPath $extractDest

Write-Host ""

# ---------------------------------------------------------------------------
# Step 2: Fix URL-encoded filenames left by the appx extraction tool.
#         Scans bundle\data for any file containing "%20" and renames it,
#         replacing each "%20" with a literal space.
# ---------------------------------------------------------------------------
Write-Host "[2/4] Fixing URL-encoded filenames in bundle\data ..." -ForegroundColor Yellow

$fixed = 0
Get-ChildItem -LiteralPath "bundle\data" | Where-Object { $_.Name -like "*%20*" } | ForEach-Object {
    $newName = $_.Name -replace "%20", " "
    Rename-Item -LiteralPath $_.FullName -NewName $newName
    Write-Host "  Renamed: $($_.Name) -> $newName"
    $fixed++
}

if ($fixed -eq 0) {
    Write-Host "  Nothing to fix." -ForegroundColor Gray
} else {
    Write-Host "  Fixed $fixed file(s)." -ForegroundColor Green
}

Write-Host ""

# ---------------------------------------------------------------------------
# Step 3: Patch game-info.json - replace the live server URL with localhost.
# ---------------------------------------------------------------------------
Write-Host "[3/4] Patching game-info.json ..." -ForegroundColor Yellow

$gameInfo = Join-Path $extractDest "game-info.json"
if (-not (Test-Path -LiteralPath $gameInfo)) {
    Write-Host "  WARNING: game-info.json not found, skipping patch." -ForegroundColor Red
} else {
    $oldUrl = "http://tk1-win.z2live.com/"
    $newUrl = "http://localhost:3300"
    $content = Get-Content -LiteralPath $gameInfo -Raw
    if ($content -match [regex]::Escape($newUrl)) {
        Write-Host "  Already patched." -ForegroundColor Gray
    } elseif ($content -match [regex]::Escape($oldUrl)) {
        $content = $content -replace [regex]::Escape($oldUrl), $newUrl
        Set-Content -LiteralPath $gameInfo -Value $content -NoNewline
        Write-Host "  Replaced server URL with $newUrl" -ForegroundColor Green
    } else {
        Write-Host "  WARNING: expected URL not found in game-info.json - manual edit may be required." -ForegroundColor Red
    }
}

Write-Host ""

# ---------------------------------------------------------------------------
# Step 4: Remove any previously installed version, then register the package
#         directly from this directory (no repacking needed).
#         Requires Developer Mode in Windows Settings -> For developers.
# ---------------------------------------------------------------------------
Write-Host "[4/4] Installing Appx ..." -ForegroundColor Yellow

$sig = Join-Path $extractDest "AppxSignature.p7x"
if (Test-Path -LiteralPath $sig) {
    Remove-Item -LiteralPath $sig -Force
    Write-Host "  Deleted AppxSignature.p7x" -ForegroundColor Gray
}

$existing = Get-AppxPackage -Name "king.com.ParadiseBay" -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "  Removing old version ($($existing.Version)) ..."
    Remove-AppxPackage -Package $existing.PackageFullName
}

Add-AppxPackage -Register "$extractDest\AppxManifest.xml" -ForceApplicationShutdown

Write-Host ""
Write-Host "Done! Launch 'Paradise Bay' from the Start menu." -ForegroundColor Green
