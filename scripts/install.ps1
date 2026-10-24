$ErrorActionPreference = "Stop"

$Repo = "skyw8/ki"
$Bin = "ki.exe"

$Arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") {
  Write-Host "no arm64 archive is published; installing the amd64 build, which runs under emulation"
}

$Version = $env:KI_VERSION
if (-not $Version) {
  $Latest = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
  $Version = $Latest.tag_name
  if (-not $Version) { throw "could not resolve the latest release; set KI_VERSION to the tag you want" }
}
$Version = $Version.TrimStart("v")

$Archive = "ki-$Version-windows-$Arch.zip"
$Base = "https://github.com/$Repo/releases/download/v$Version"
$Dest = if ($env:KI_BIN_DIR) { $env:KI_BIN_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\Ki" }
$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("ki-install-" + [guid]::NewGuid().ToString("N"))

try {
  New-Item -ItemType Directory -Force $Tmp | Out-Null
  try {
    Invoke-WebRequest "$Base/$Archive" -OutFile (Join-Path $Tmp $Archive)
    Invoke-WebRequest "$Base/checksums.txt" -OutFile (Join-Path $Tmp "checksums.txt")
  } catch {
    throw "could not download from $Base; check that v$Version publishes a windows-$Arch archive ($_)"
  }

  $Line = Get-Content (Join-Path $Tmp "checksums.txt") |
    Where-Object { $_ -match ([regex]::Escape($Archive) + '$') } |
    Select-Object -First 1
  if (-not $Line) { throw "checksums.txt of v$Version has no entry for $Archive" }
  $Expected = ($Line -split '\s+')[0].ToLower()
  $Actual = (Get-FileHash (Join-Path $Tmp $Archive) -Algorithm SHA256).Hash.ToLower()
  if ($Expected -ne $Actual) { throw "checksum mismatch for ${Archive}: expected $Expected, got $Actual" }

  Expand-Archive (Join-Path $Tmp $Archive) -DestinationPath $Tmp -Force
  $Source = Join-Path $Tmp "ki-$Version-windows-$Arch\$Bin"
  if (-not (Test-Path $Source)) { throw "$Archive does not contain $Bin" }

  New-Item -ItemType Directory -Force $Dest | Out-Null
  try {
    Copy-Item $Source (Join-Path $Dest $Bin) -Force
  } catch {
    throw "could not replace $(Join-Path $Dest $Bin) because a ki server is running from it; run 'ki stop' and re-run this installer ($_)"
  }
} finally {
  Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}

Write-Host "installed ki $Version to $(Join-Path $Dest $Bin)"

$KiHome = if ($env:KI_HOME) { $env:KI_HOME } else { Join-Path $env:USERPROFILE ".ki" }
if (Test-Path (Join-Path $KiHome "server.json")) {
  Write-Host ""
  Write-Host "a ki server is running from the previous build; activate $Version with:"
  Write-Host "  `"$(Join-Path $Dest $Bin)`" restart"
}
$UserPath = [Environment]::GetEnvironmentVariable("PATH", "User")
$Parts = @($UserPath -split ';' | Where-Object { $_ })
if ($Parts -notcontains $Dest) {
  [Environment]::SetEnvironmentVariable("PATH", (@($Parts) + $Dest) -join ';', "User")
  Write-Host "added $Dest to the user PATH; open a new terminal to run ki"
}
