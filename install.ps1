# Airskills CLI installer for Windows
# Usage: irm https://airskills.ai/install.ps1 | iex

$ErrorActionPreference = "Stop"
$repo = "chrismdp/airskills"
$binary = "airskills.exe"
$installDir = "$env:LOCALAPPDATA\airskills"

Write-Host "`nAirskills CLI installer`n" -ForegroundColor Green

# Detect architecture
$arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else {
    Write-Error "32-bit Windows is not supported"; exit 1
}
$platform = "windows_$arch"
Write-Host "Platform: $platform" -ForegroundColor Green

# Get latest release
Write-Host "Fetching latest release..." -ForegroundColor Green
$release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
$version = $release.tag_name
Write-Host "Version: $version" -ForegroundColor Green

$archive = "airskills_${platform}.tar.gz"
$url = "https://github.com/$repo/releases/download/$version/$archive"
$checksumUrl = "https://github.com/$repo/releases/download/$version/checksums.txt"

# Download
$tmp = New-TemporaryFile | Rename-Item -NewName { $_.Name + ".tar.gz" } -PassThru
Write-Host "Downloading $archive..." -ForegroundColor Green
Invoke-WebRequest -Uri $url -OutFile $tmp.FullName

# Verify checksum
try {
    $checksums = Invoke-RestMethod $checksumUrl
    $expected = ($checksums -split "`n" | Where-Object { $_ -match $archive } | ForEach-Object { ($_ -split "\s+")[0] })
    if ($expected) {
        $actual = (Get-FileHash $tmp.FullName -Algorithm SHA256).Hash.ToLower()
        if ($actual -ne $expected) {
            Write-Error "Checksum mismatch! Expected: $expected Got: $actual"
            exit 1
        }
        Write-Host "Checksum verified." -ForegroundColor Green
    }
} catch {
    Write-Host "Skipping checksum verification." -ForegroundColor Yellow
}

# Extract
$extractDir = Join-Path $env:TEMP "airskills-install"
if (Test-Path $extractDir) { Remove-Item $extractDir -Recurse -Force }
New-Item -ItemType Directory -Path $extractDir | Out-Null
tar -xzf $tmp.FullName -C $extractDir
$exe = Get-ChildItem -Path $extractDir -Recurse -Filter $binary | Select-Object -First 1

if (-not $exe) {
    Write-Error "Binary not found in archive"
    exit 1
}

# Install
if (-not (Test-Path $installDir)) { New-Item -ItemType Directory -Path $installDir | Out-Null }
Copy-Item $exe.FullName (Join-Path $installDir $binary) -Force
Write-Host "Installed to $installDir\$binary" -ForegroundColor Green

# Add to PATH if needed
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$installDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
    Write-Host "Added $installDir to your PATH. Restart your terminal to use 'airskills'." -ForegroundColor Yellow
}

# Cleanup
Remove-Item $tmp.FullName -Force -ErrorAction SilentlyContinue
Remove-Item $extractDir -Recurse -Force -ErrorAction SilentlyContinue

Write-Host "`nDone! Run 'airskills login' to get started.`n" -ForegroundColor Green
