param(
    [string]$Version = "0.1.0",
    [switch]$Pack
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$publish = Join-Path $root "dist\desktop"
$exe = Join-Path $publish "ClipBridge.exe"

New-Item -ItemType Directory -Force -Path $publish | Out-Null
go build -trimpath -ldflags="-s -w -H windowsgui" -o $exe ./desktop

Copy-Item -LiteralPath (Join-Path $root "clipbridge.ico") -Destination (Join-Path $publish "clipbridge.ico") -Force

if ($Pack) {
    vpk pack `
        --packId ClipBridge `
        --packVersion $Version `
        --packDir $publish `
        --mainExe ClipBridge.exe `
        --packTitle ClipBridge `
        --packAuthors "Blake Becker" `
        --icon (Join-Path $root "clipbridge.ico") `
        --outputDir (Join-Path $root "Releases")
}
