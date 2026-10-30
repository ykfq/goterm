# Launches the built app, waits for its window, captures it to a PNG with
# PrintWindow(PW_RENDERFULLCONTENT) — the only way to copy WebView2's
# composited surface from a script — then shuts the app down by PID.
#
#   .\tools\gui-check.ps1                 # -> build\gui-welcome.png
#   .\tools\gui-check.ps1 -Keep           # leave the window open
#
# WebView2 ignores WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS here (verified: no
# --remote-debugging-port reaches msedgewebview2), so this is a screenshot
# check, not a CDP-driven one.
param(
    [string]$Exe = "$PSScriptRoot\..\bin\goterm.exe",
    [string]$Out = "$PSScriptRoot\..\build\gui-welcome.png",
    [int]$TimeoutSeconds = 20,
    [switch]$Keep
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Win32Shot {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hWnd, IntPtr hdcBlt, uint nFlags);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT lpRect);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
}
"@

if (-not (Test-Path $Exe)) { throw "not built: $Exe (run: wails3 build)" }
$Exe = (Resolve-Path $Exe).Path
$Out = [System.IO.Path]::GetFullPath($Out)

# The v3 app is single-instance: a second launch hands the activation to the
# first process and exits 0, which would otherwise read as "crash on startup".
$appName = [System.IO.Path]::GetFileNameWithoutExtension($Exe)
$stale = Get-Process -Name $appName -ErrorAction SilentlyContinue
if ($stale) {
    $stale | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 800
    "stopped $($stale.Count) already-running instance(s) of $appName"
}

$proc = Start-Process -FilePath $Exe -PassThru
"launched pid=$($proc.Id)"

$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
$hwnd = [IntPtr]::Zero
while ((Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 400
    $proc.Refresh()
    if ($proc.HasExited) { throw "the app exited during startup with code $($proc.ExitCode)" }
    if ($proc.MainWindowHandle -ne [IntPtr]::Zero) { $hwnd = $proc.MainWindowHandle; break }
}
if ($hwnd -eq [IntPtr]::Zero) { throw "the app never created a window within $TimeoutSeconds s" }
"window handle=$hwnd (WebView2 process is up)"

# Give the webview a moment to paint the first frame, then capture.
[void][Win32Shot]::ShowWindow($hwnd, 9)   # SW_RESTORE
[void][Win32Shot]::SetForegroundWindow($hwnd)
Start-Sleep -Seconds 3

$rect = New-Object Win32Shot+RECT
[void][Win32Shot]::GetWindowRect($hwnd, [ref]$rect)
$width = [Math]::Max(1, $rect.Right - $rect.Left)
$height = [Math]::Max(1, $rect.Bottom - $rect.Top)

function Capture([IntPtr]$Hwnd, [int]$Width, [int]$Height) {
    $bitmap = New-Object System.Drawing.Bitmap($Width, $Height, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $hdc = $graphics.GetHdc()
    $ok = [Win32Shot]::PrintWindow($Hwnd, $hdc, 2)   # 2 = PW_RENDERFULLCONTENT
    $graphics.ReleaseHdc($hdc)
    $graphics.Dispose()
    return @{ Bitmap = $bitmap; Ok = $ok }
}

function CountColours($Bitmap, [int]$Width, [int]$Height) {
    $seen = @{}
    for ($x = 0; $x -lt $Width; $x += 17) {
        for ($y = 0; $y -lt $Height; $y += 17) { $seen[$Bitmap.GetPixel($x, $y).ToArgb()] = $true }
    }
    return $seen.Count
}

# A window that painted nothing is indistinguishable from a white image. The
# first paint after a fresh build can take longer than any fixed wait (WebView2
# starts cold), so capture until the result looks like a UI instead of an empty
# surface — a fixed 3 s wait once produced a pass on a 9-colour blank capture.
$shot = $null
$distinct = 0
for ($attempt = 1; $attempt -le 6; $attempt++) {
    if ($shot) { $shot.Bitmap.Dispose() }
    $shot = Capture $hwnd $width $height
    $distinct = CountColours $shot.Bitmap $width $height
    if ($distinct -ge 20) { break }
    if ($attempt -lt 6) { Start-Sleep -Milliseconds 1500 }
}
$bitmap = $shot.Bitmap
$bitmap.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
$bitmap.Dispose()

"captured ${width}x${height} PrintWindow=$($shot.Ok) distinctColours=$distinct -> $Out"

if (-not $Keep) {
    Stop-Process -Id $proc.Id -Force
    "stopped pid=$($proc.Id)"
} else {
    "left running: pid=$($proc.Id)"
}

if ($distinct -lt 20) { throw "the capture looks blank ($distinct distinct colours after 6 attempts)" }
