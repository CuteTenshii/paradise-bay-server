$ErrorActionPreference = "Stop"
Set-Location -LiteralPath $PSScriptRoot

Write-Host "=== Paradise Bay Installer ===" -ForegroundColor Cyan
Write-Host ""

# ---------------------------------------------------------------------------
# Step 1: Fix URL-encoded filenames left by the appx extraction tool.
#         Scans bundle\data for any file containing "%20" and renames it,
#         replacing each "%20" with a literal space.
# ---------------------------------------------------------------------------
Write-Host "[1/2] Fixing URL-encoded filenames in bundle\data ..." -ForegroundColor Yellow

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
# Step 2: Remove any previously installed version, then register the package
#         directly from this directory (no repacking needed).
#         Requires Developer Mode in Windows Settings -> For developers.
# ---------------------------------------------------------------------------
Write-Host "[2/2] Installing Appx ..." -ForegroundColor Yellow

$existing = Get-AppxPackage -Name "king.com.ParadiseBay" -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "  Removing old version ($($existing.Version)) ..."
    Remove-AppxPackage -Package $existing.PackageFullName
}

Add-AppxPackage -Register "$PSScriptRoot\AppxManifest.xml" -ForceApplicationShutdown

Write-Host ""
Write-Host "Done! Launch 'Paradise Bay' from the Start menu." -ForegroundColor Green
