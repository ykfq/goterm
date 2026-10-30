# Drives a real trzsz transfer through the built app, the way a user does it:
# connect, type `tsz demo.bin` (or `trz`), and confirm the native Explorer
# dialog that must appear.
#
#   .\tools\gui-transfer.ps1 -Direction download
#   .\tools\gui-transfer.ps1 -Direction upload
#
# Exit code is 0 only when the file actually arrived byte-for-byte, with the
# screenshots left in build\ for inspection:
#   gui-transfer-dialog.png   the native dialog, cropped to its window
#   gui-transfer-screen.png   the desktop at that moment (shows it is on top)
#   gui-transfer-done.png     the terminal after the transfer
param(
    [ValidateSet('download', 'upload')]
    [string]$Direction = 'download',
    [string]$Exe = "$PSScriptRoot\..\bin\goterm.exe",
    [int]$Port = 47333,
    [string]$User = 'tester',
    [string]$Password = 'goterm-dev',
    [int]$DialogTimeoutSec = 20,
    [switch]$Manual,
    [switch]$Keep
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.Windows.Forms

Add-Type @"
using System;
using System.Runtime.InteropServices;
using System.Text;
public class XferWin32 {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left, Top, Right, Bottom; }
  public delegate bool EnumProc(IntPtr hWnd, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc cb, IntPtr lParam);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
  [DllImport("user32.dll")] public static extern int GetClassName(IntPtr hWnd, StringBuilder name, int max);
  [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowTextW(IntPtr hWnd, StringBuilder text, int max);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] public static extern bool SwitchToThisWindow(IntPtr hWnd, bool alternate);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
  [DllImport("user32.dll")] public static extern bool AttachThreadInput(uint attach, uint attachTo, bool attach);
  [DllImport("kernel32.dll")] public static extern uint GetCurrentThreadId();
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int cmd);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hWnd, IntPtr hdc, uint flags);
  [DllImport("user32.dll")] public static extern void keybd_event(byte vk, byte scan, uint flags, UIntPtr extra);
  public const uint LEFTDOWN = 0x0002, LEFTUP = 0x0004, RIGHTDOWN = 0x0008, RIGHTUP = 0x0010;
  public const uint KEYUP = 0x0002;
  public const byte VK_CTRL = 0x11, VK_SHIFT = 0x10, VK_RETURN = 0x0D, VK_ESCAPE = 0x1B, VK_TAB = 0x09, VK_T = 0x54;

  public static void Click(int x, int y, bool right = false) {
    SetCursorPos(x, y);
    System.Threading.Thread.Sleep(120);
    mouse_event(right ? RIGHTDOWN : LEFTDOWN, 0, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(60);
    mouse_event(right ? RIGHTUP : LEFTUP, 0, 0, 0, UIntPtr.Zero);
  }

  // SetForegroundWindow alone fails when the caller is not already the
  // foreground process (Windows refuses it), so attach to the current
  // foreground thread first. Running this script from a background service or
  // another session cannot be fixed this way — it needs a real desktop.
  public static bool Focus(IntPtr hwnd) {
    uint foregroundPid;
    uint foregroundThread = GetWindowThreadProcessId(GetForegroundWindow(), out foregroundPid);
    uint mine = GetCurrentThreadId();
    bool attached = AttachThreadInput(mine, foregroundThread, true);
    keybd_event(0x12, 0, 0, UIntPtr.Zero);          // Alt tap: unlocks SetForegroundWindow
    keybd_event(0x12, 0, KEYUP, UIntPtr.Zero);
    SwitchToThisWindow(hwnd, true);
    bool ok = SetForegroundWindow(hwnd);
    AttachThreadInput(mine, foregroundThread, false);
    return ok && GetForegroundWindow() == hwnd && attached;
  }
  public static void Chord(byte modifier, byte key) {
    keybd_event(modifier, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(key, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(key, 0, KEYUP, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(modifier, 0, KEYUP, UIntPtr.Zero);
    System.Threading.Thread.Sleep(60);
  }
  public static void Chord2(byte modifierA, byte modifierB, byte key) {
    keybd_event(modifierA, 0, 0, UIntPtr.Zero);
    keybd_event(modifierB, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(key, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(key, 0, KEYUP, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(modifierB, 0, KEYUP, UIntPtr.Zero);
    keybd_event(modifierA, 0, KEYUP, UIntPtr.Zero);
    System.Threading.Thread.Sleep(60);
  }
  public static void Tap(byte key) {
    keybd_event(key, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(40);
    keybd_event(key, 0, KEYUP, UIntPtr.Zero);
    System.Threading.Thread.Sleep(60);
  }

  // Top-level windows belonging to one process, with class and title.
  public static string[] WindowsOf(uint pid) {
    var lines = new System.Collections.Generic.List<string>();
    EnumWindows((h, l) => {
      uint owner;
      GetWindowThreadProcessId(h, out owner);
      if (owner != pid || !IsWindowVisible(h)) return true;
      var cls = new StringBuilder(64);
      GetClassName(h, cls, cls.Capacity);
      var title = new StringBuilder(256);
      GetWindowTextW(h, title, title.Capacity);
      lines.Add(h.ToInt64() + "|" + cls + "|" + title);
      return true;
    }, IntPtr.Zero);
    return lines.ToArray();
  }
}
"@

$root = (Resolve-Path "$PSScriptRoot\..").Path
$Exe = (Resolve-Path $Exe).Path

# The app takes a single-instance lock, so a leftover window from an earlier
# run would silently swallow this one (the launch just exits 0).
$stale = @(Get-Process goterm -ErrorAction SilentlyContinue)
if ($stale.Count -gt 0) {
    throw "goterm is already running (pid $($stale.Id -join ', ')); close it first — a second launch would be redirected to that instance and this script would drive the wrong window"
}

function Save-Window([IntPtr]$Handle, [string]$Path) {
    $rect = New-Object XferWin32+RECT
    [void][XferWin32]::GetWindowRect($Handle, [ref]$rect)
    $w = [Math]::Max(1, $rect.Right - $rect.Left)
    $h = [Math]::Max(1, $rect.Bottom - $rect.Top)
    $bmp = New-Object System.Drawing.Bitmap($w, $h, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $hdc = $g.GetHdc()
    $ok = [XferWin32]::PrintWindow($Handle, $hdc, 2)
    $g.ReleaseHdc($hdc)
    $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    "{0} {1}x{2} PrintWindow={3}" -f (Split-Path $Path -Leaf), $w, $h, $ok
}

function Save-Screen([string]$Path) {
    $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $bmp = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen($bounds.X, $bounds.Y, 0, 0, $bounds.Size)
    $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    "desktop -> {0}" -f (Split-Path $Path -Leaf)
}

function Set-Text([int]$X, [int]$Y, [string]$Value) {
    [XferWin32]::Click($X, $Y)
    Start-Sleep -Milliseconds 200
    Set-Clipboard -Value $Value
    [XferWin32]::Chord([XferWin32]::VK_CTRL, 0x41)
    Start-Sleep -Milliseconds 80
    [XferWin32]::Chord([XferWin32]::VK_CTRL, 0x56)
    Start-Sleep -Milliseconds 200
}

# Paste into whatever has focus — the way the connect form is filled in.
function Paste-Focused([string]$Value) {
    Set-Clipboard -Value $Value
    Start-Sleep -Milliseconds 120
    [XferWin32]::Chord([XferWin32]::VK_CTRL, 0x56)
    Start-Sleep -Milliseconds 250
}

# Waits for the native Explorer dialog: a #32770 window in the app's process.
function Wait-FileDialog([int]$ProcessId, [int]$TimeoutSec) {
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        foreach ($line in [XferWin32]::WindowsOf([uint32]$ProcessId)) {
            $parts = $line.Split('|')
            if ($parts[1] -eq '#32770') {
                return [IntPtr][Int64]$parts[0]
            }
        }
        Start-Sleep -Milliseconds 250
    }
    return [IntPtr]::Zero
}

$workdir = Join-Path $env:TEMP ("goterm-xfer-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
$transferDir = Join-Path $workdir 'remote'
$targetDir = Join-Path $workdir 'local'
New-Item -ItemType Directory -Force -Path $transferDir, $targetDir | Out-Null

# A known file for the upload direction: 100 KiB of a reproducible pattern.
$uploadSource = Join-Path $workdir 'upload-src.bin'
$sourceBytes = New-Object byte[] 102400
for ($i = 0; $i -lt $sourceBytes.Length; $i++) { $sourceBytes[$i] = [byte](($i * 13 + 3) -band 0xff) }
[System.IO.File]::WriteAllBytes($uploadSource, $sourceBytes)
$sourceHash = (Get-FileHash -Algorithm MD5 $uploadSource).Hash

$env:GOTERM_DIR = Join-Path $workdir 'config'
$env:GOTERM_SECRETS = 'file'
New-Item -ItemType Directory -Force -Path $env:GOTERM_DIR | Out-Null

$server = Start-Process -FilePath (Join-Path $root 'build\bin\sshtest.exe') `
    -ArgumentList @('-addr', "127.0.0.1:$Port", '-user', $User, '-password', $Password, '-shell', 'trzsz',
                    '-transfer-dir', $transferDir, '-hostkey', (Join-Path $workdir 'host_key.pem')) `
    -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 1
if ($server.HasExited) { throw 'sshtest failed to start' }
"sshtest pid=$($server.Id) on $Port, transfer dir $transferDir"

$app = Start-Process -FilePath $Exe -PassThru
$exitCode = 1
try {
    $deadline = (Get-Date).AddSeconds(30)
    $hwnd = [IntPtr]::Zero
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 400
        $app.Refresh()
        if ($app.HasExited) { throw "the app exited with code $($app.ExitCode)" }
        if ($app.MainWindowHandle -ne [IntPtr]::Zero) { $hwnd = $app.MainWindowHandle; break }
    }
    if ($hwnd -eq [IntPtr]::Zero) { throw 'no app window appeared' }

    [void][XferWin32]::ShowWindow($hwnd, 9)
    if (-not [XferWin32]::Focus($hwnd)) {
        "warning: could not take the foreground — keystrokes may go elsewhere. Run this script from the desktop session, or use -Manual."
    }
    Start-Sleep -Seconds 1

    $rect = New-Object XferWin32+RECT
    [void][XferWin32]::GetWindowRect($hwnd, [ref]$rect)
    "window $($rect.Right - $rect.Left)x$($rect.Bottom - $rect.Top) at $($rect.Left),$($rect.Top)"

    if ($Manual) {
        [void][XferWin32]::Focus($hwnd)
        ""
        "Now drive the app yourself: Ctrl+T to connect to 127.0.0.1:$Port as $User, then run"
        if ($Direction -eq 'download') {
            "    tsz demo.bin          and pick any folder for the 256 KiB demo file"
        } else {
            "    trz                   and pick $uploadSource"
        }
        "Press Enter here afterwards to verify what arrived."
        [void](Read-Host)
        $expected = if ($Direction -eq 'download') { Join-Path $targetDir 'demo.bin' } else { Join-Path $transferDir 'upload-src.bin' }
        if (-not (Test-Path $expected)) { throw "nothing arrived at $expected" }
        if ($Direction -eq 'download') {
            $bytes = [System.IO.File]::ReadAllBytes($expected)
            if ($bytes.Length -ne 262144) { throw "download is $($bytes.Length) bytes, want 262144" }
            for ($i = 0; $i -lt $bytes.Length; $i++) {
                if ($bytes[$i] -ne [byte](($i * 7 + 11) -band 0xff)) { throw "download differs at byte $i" }
            }
            "download verified: $expected, 262144 bytes, byte-exact"
        } else {
            $got = (Get-FileHash -Algorithm MD5 $expected).Hash
            if ($got -ne $sourceHash) { throw "upload md5 $got, want $sourceHash" }
            "upload verified: $expected, $((Get-Item $expected).Length) bytes, md5 matches"
        }
        exit 0
    }

    # --- connect ---------------------------------------------------------
    # Ctrl+T opens Quick connect with the host field focused, so the whole form
    # can be filled with Tab and the clipboard: no pixel coordinates that break
    # whenever the layout moves.
    [XferWin32]::Chord([XferWin32]::VK_CTRL, [XferWin32]::VK_T)
    Start-Sleep -Milliseconds 900
    $form = @('127.0.0.1', "$Port", $User, $Password)
    for ($i = 0; $i -lt $form.Count; $i++) {
        Paste-Focused $form[$i]
        if ($i -eq 0) { [XferWin32]::Tap([XferWin32]::VK_TAB) }                      # host -> port
        elseif ($i -eq 1) { [XferWin32]::Tap([XferWin32]::VK_TAB) }                  # port -> user
        elseif ($i -eq 2) {                                                          # user -> secret
            [XferWin32]::Tap([XferWin32]::VK_TAB); [XferWin32]::Tap([XferWin32]::VK_TAB); [XferWin32]::Tap([XferWin32]::VK_TAB)
        }
        Start-Sleep -Milliseconds 200
    }
    Save-Window $hwnd (Join-Path $root 'build\gui-xfer-form.png') | Out-Null
    [XferWin32]::Tap([XferWin32]::VK_RETURN)
    Start-Sleep -Seconds 4
    Save-Window $hwnd (Join-Path $root 'build\gui-xfer-session.png') | Out-Null

    # --- run the transfer command ----------------------------------------
    # A connected tab focuses its terminal, so the app's paste binding reaches
    # the session without another click.
    Set-Clipboard -Value 'tsz demo.bin'
    if ($Direction -eq 'upload') { Set-Clipboard -Value 'trz' }
    [XferWin32]::Chord2([XferWin32]::VK_CTRL, [XferWin32]::VK_SHIFT, 0x56)   # the app's paste
    Start-Sleep -Milliseconds 400
    [XferWin32]::Tap([XferWin32]::VK_RETURN)
    Start-Sleep -Seconds 1
    Save-Window $hwnd (Join-Path $root 'build\gui-xfer-banner.png') | Out-Null

    # --- the dialog that the bug report said never appeared --------------
    $dialog = Wait-FileDialog $app.Id $DialogTimeoutSec
    if ($dialog -eq [IntPtr]::Zero) {
        Save-Window $hwnd (Join-Path $root 'build\gui-transfer-done.png') | Out-Null
        $windows = [XferWin32]::WindowsOf([uint32]$app.Id) -join '; '
        throw "no Explorer dialog appeared within ${DialogTimeoutSec}s. Windows in the app: $windows"
    }
    [void][XferWin32]::Focus($dialog)
    Start-Sleep -Milliseconds 500
    Save-Window $dialog (Join-Path $root 'build\gui-transfer-dialog.png')
    Save-Screen (Join-Path $root 'build\gui-transfer-screen.png')
    "dialog appeared: $([XferWin32]::WindowsOf([uint32]$app.Id) | Where-Object { $_ -like "*#32770*" })"

    # --- pick the destination / the file ---------------------------------
    $choice = if ($Direction -eq 'download') { $targetDir } else { $uploadSource }
    Set-Clipboard -Value $choice
    [XferWin32]::Chord([XferWin32]::VK_CTRL, 0x56)   # native edit: plain Ctrl+V
    Start-Sleep -Milliseconds 500
    [XferWin32]::Tap([XferWin32]::VK_RETURN)
    Start-Sleep -Milliseconds 800
    # A folder picker sometimes needs a second Enter on "Select Folder".
    $again = Wait-FileDialog $app.Id 3
    if ($again -ne [IntPtr]::Zero) {
        [void][XferWin32]::Focus($again)
        Start-Sleep -Milliseconds 300
        [XferWin32]::Tap([XferWin32]::VK_RETURN)
    }

    # --- wait for the protocol to finish ---------------------------------
    $expected = if ($Direction -eq 'download') { Join-Path $targetDir 'demo.bin' } else { Join-Path $transferDir 'upload-src.bin' }
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 400
        if (Test-Path $expected) {
            $size = (Get-Item $expected).Length
            if ($size -gt 0) { break }
        }
    }
    [void][XferWin32]::Focus($hwnd)
    Start-Sleep -Seconds 1
    Save-Window $hwnd (Join-Path $root 'build\gui-transfer-done.png') | Out-Null

    if (-not (Test-Path $expected)) { throw "nothing arrived at $expected" }

    if ($Direction -eq 'download') {
        # The server generates byte((i*7+11) & 0xff), 262144 bytes.
        $bytes = [System.IO.File]::ReadAllBytes($expected)
        if ($bytes.Length -ne 262144) { throw "download is $($bytes.Length) bytes, want 262144" }
        for ($i = 0; $i -lt $bytes.Length; $i++) {
            if ($bytes[$i] -ne [byte](($i * 7 + 11) -band 0xff)) { throw "download differs at byte $i" }
        }
        "download verified: $expected, 262144 bytes, byte-exact"
    } else {
        $got = (Get-FileHash -Algorithm MD5 $expected).Hash
        if ($got -ne $sourceHash) { throw "upload md5 $got, want $sourceHash" }
        "upload verified: $expected, $((Get-Item $expected).Length) bytes, md5 matches"
    }
    $exitCode = 0
}
catch {
    "FAILED: $($_.Exception.Message)"
}
finally {
    try { $app.Refresh(); if (-not $app.HasExited) { [void][XferWin32]::SetForegroundWindow(0) } } catch {}
    foreach ($p in @($app, $server)) {
        try { $p.Refresh(); if (-not $p.HasExited) { Start-Sleep -Milliseconds 200; Stop-Process -Id $p.Id -ErrorAction SilentlyContinue } } catch {}
    }
    if ($Keep) { "kept: $workdir" } else { Remove-Item -Recurse -Force $workdir -ErrorAction SilentlyContinue }
}
exit $exitCode
