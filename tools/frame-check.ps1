# Asks Windows what it thinks the app's window frame looks like.
#
# The title bar and the hairline border are drawn by the OS, not by the webview,
# so a screenshot of the app's own surface (PrintWindow, what gui-check.ps1
# takes) shows the *legacy* frame and can report a white title bar even when the
# composited one is dark. The DWM attributes are the honest answer:
#
#   DWMWA_USE_IMMERSIVE_DARK_MODE (20) -> 1 for the dark theme, 0 for the light one
#   DWMWA_CAPTION_COLOR (35)           -> the caption colour, Windows 11 only
#
#   .\tools\frame-check.ps1
#
# Exits non-zero if the frame does not match the theme the app is running.
param(
    [string]$Exe = "$PSScriptRoot\..\bin\goterm.exe",
    [ValidateSet('dark', 'light')][string]$Expect = 'dark',
    [int]$TimeoutSeconds = 25
)

$ErrorActionPreference = 'Stop'

Add-Type @"
using System;
using System.Runtime.InteropServices;
public class DwmProbe {
  [DllImport("dwmapi.dll")] public static extern int DwmGetWindowAttribute(IntPtr hwnd, int attr, out int value, int size);
}
"@

if (-not (Test-Path $Exe)) { throw "not built: $Exe (run: wails3 build)" }
$Exe = (Resolve-Path $Exe).Path

$stale = Get-Process -Name ([System.IO.Path]::GetFileNameWithoutExtension($Exe)) -ErrorAction SilentlyContinue
if ($stale) {
    $stale | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 600
}

$proc = Start-Process -FilePath $Exe -PassThru
$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
$hwnd = [IntPtr]::Zero
while ((Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 300
    $proc.Refresh()
    if ($proc.HasExited) { throw "the app exited during startup with code $($proc.ExitCode)" }
    if ($proc.MainWindowHandle -ne [IntPtr]::Zero) { $hwnd = $proc.MainWindowHandle; break }
}
if ($hwnd -eq [IntPtr]::Zero) { throw "the app never created a window within $TimeoutSeconds s" }

# The theme lives in the webview's localStorage, so the frame is painted a moment
# after the first frame of the UI.
Start-Sleep -Seconds 4

$dark = 0
$rc = [DwmProbe]::DwmGetWindowAttribute($hwnd, 20, [ref]$dark, 4)
"hwnd=$hwnd DWMWA_USE_IMMERSIVE_DARK_MODE rc=$rc value=$dark (expected $(if ($Expect -eq 'dark') { 1 } else { 0 }))"

$caption = 0
$rc2 = [DwmProbe]::DwmGetWindowAttribute($hwnd, 35, [ref]$caption, 4)
if ($rc2 -eq 0) {
    # 0x00BBGGRR, the layout Windows uses for colours.
    $r = $caption -band 0xff
    $g = ($caption -shr 8) -band 0xff
    $b = ($caption -shr 16) -band 0xff
    "DWMWA_CAPTION_COLOR = #{0:x2}{1:x2}{2:x2}" -f $r, $g, $b
} else {
    "DWMWA_CAPTION_COLOR not supported on this build (Windows 10 / Server); the dark-mode flag above is the whole fix here"
}

Stop-Process -Id $proc.Id -Force
"stopped pid=$($proc.Id)"

$want = if ($Expect -eq 'dark') { 1 } else { 0 }
if ($dark -ne $want) { throw "the window frame does not match the $Expect theme (got value=$dark, want $want)" }
