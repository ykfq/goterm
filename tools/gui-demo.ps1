# Drives the built app the way a user does and captures the result:
#   Quick connect -> connect -> type in the terminal -> right-click menu.
#
#   .\tools\gui-demo.ps1                        # -> build\gui-{dialog,session,typed,menu}.png
#   .\tools\gui-demo.ps1 -ClickItem 700,476      # additionally click a menu item at that
#                                                # window coordinate -> build\gui-menu-after.png
#   .\tools\gui-demo.ps1 -ScreenShot build\gui-dialog-full.png -ClickItem 700,506
#                                                # full-screen capture (e.g. a native file dialog)
#
# Coordinates are window-relative and were measured from the captures this
# script produces. It runs against a throwaway GOTERM_DIR so it never touches
# real host pins.
param(
    [string]$Exe = "$PSScriptRoot\..\bin\goterm.exe",
    [int]$Port = 47222,
    [string]$User = 'tester',
    [string]$Password = 'goterm-dev',
    [string]$ClickItem = '',
    [string]$ScreenShot = '',
    [switch]$Keep
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing
Add-Type -AssemblyName System.Windows.Forms

Add-Type @"
using System;
using System.Runtime.InteropServices;
public class Win32Drive {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT lpRect);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] public static extern bool SwitchToThisWindow(IntPtr hWnd, bool alternate);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
  [DllImport("user32.dll")] public static extern bool AttachThreadInput(uint attach, uint attachTo, bool attach);
  [DllImport("kernel32.dll")] public static extern uint GetCurrentThreadId();
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr hWnd, IntPtr hdc, uint flags);
  public const uint LEFTDOWN = 0x0002;
  public const uint LEFTUP = 0x0004;
  public const uint RIGHTDOWN = 0x0008;
  public const uint RIGHTUP = 0x0010;
  public static void Click(int x, int y, bool right = false) {
    SetCursorPos(x, y);
    System.Threading.Thread.Sleep(120);
    uint down = right ? RIGHTDOWN : LEFTDOWN;
    uint up = right ? RIGHTUP : LEFTUP;
    mouse_event(down, 0, 0, 0, UIntPtr.Zero);
    System.Threading.Thread.Sleep(60);
    mouse_event(up, 0, 0, 0, UIntPtr.Zero);
  }
  // SendKeys throws "Access is denied" (UIPI) in this environment; raw
  // keybd_event calls to the focused window are what actually work.
  [DllImport("user32.dll")] public static extern void keybd_event(byte vk, byte scan, uint flags, UIntPtr extra);
  public const uint KEYUP = 0x0002;
  public const byte VK_CTRL = 0x11;
  public const byte VK_SHIFT = 0x10;
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

  // SetForegroundWindow alone is refused when the caller is not already the
  // foreground process; attaching to the foreground thread first is the
  // documented way around it, and it still needs a real desktop session.
  public static bool Focus(IntPtr hwnd) {
    uint pid;
    uint fgThread = GetWindowThreadProcessId(GetForegroundWindow(), out pid);
    uint mine = GetCurrentThreadId();
    bool attached = AttachThreadInput(mine, fgThread, true);
    keybd_event(0x12, 0, 0, UIntPtr.Zero);
    keybd_event(0x12, 0, KEYUP, UIntPtr.Zero);
    SwitchToThisWindow(hwnd, true);
    bool ok = SetForegroundWindow(hwnd);
    AttachThreadInput(mine, fgThread, false);
    return attached && ok && GetForegroundWindow() == hwnd;
  }
}
"@

$root = (Resolve-Path "$PSScriptRoot\..").Path
$Exe = (Resolve-Path $Exe).Path

function Save-Window([IntPtr]$Handle, [string]$Path) {
    $rect = New-Object Win32Drive+RECT
    [void][Win32Drive]::GetWindowRect($Handle, [ref]$rect)
    $w = [Math]::Max(1, $rect.Right - $rect.Left)
    $h = [Math]::Max(1, $rect.Bottom - $rect.Top)
    $bmp = New-Object System.Drawing.Bitmap($w, $h, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $hdc = $g.GetHdc()
    $ok = [Win32Drive]::PrintWindow($Handle, $hdc, 2)
    $g.ReleaseHdc($hdc)
    $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    "$(Split-Path $Path -Leaf) ${w}x${h} PrintWindow=$ok"
}

function Save-Screen([string]$Path) {
    $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $bmp = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen($bounds.X, $bounds.Y, 0, 0, $bounds.Size)
    $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    "full screen $($bounds.Width)x$($bounds.Height) -> $(Split-Path $Path -Leaf)"
}

# Paste rather than type: keystroke timing races the webview and drops characters.
$script:savedClipboard = Get-Clipboard -Raw -ErrorAction SilentlyContinue

# Paste into whatever has focus. Safer than clicking at guessed coordinates:
# the form is filled with Tab, which survives a layout change.
function Paste-Focused([string]$Value) {
    Set-Clipboard -Value $Value
    Start-Sleep -Milliseconds 120
    [Win32Drive]::Chord([Win32Drive]::VK_CTRL, 0x56)
    Start-Sleep -Milliseconds 250
}

$workdir = Join-Path $env:TEMP ("goterm-gui-demo-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Force -Path $workdir | Out-Null
$env:GOTERM_DIR = $workdir
$env:GOTERM_SECRETS = 'file'
"config dir: $workdir"

$server = Start-Process -FilePath (Join-Path $root 'build\bin\sshtest.exe') `
    -ArgumentList @('-addr', "127.0.0.1:$Port", '-user', $User, '-password', $Password, '-hostkey', (Join-Path $workdir 'host_key.pem')) `
    -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 1
if ($server.HasExited) { throw 'sshtest failed to start' }
"sshtest pid=$($server.Id) on $Port"

$app = Start-Process -FilePath $Exe -PassThru
try {
    $deadline = (Get-Date).AddSeconds(25)
    $hwnd = [IntPtr]::Zero
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 400
        $app.Refresh()
        if ($app.HasExited) { throw "the app exited with code $($app.ExitCode)" }
        if ($app.MainWindowHandle -ne [IntPtr]::Zero) { $hwnd = $app.MainWindowHandle; break }
    }
    if ($hwnd -eq [IntPtr]::Zero) { throw 'no window appeared' }

    [void][Win32Drive]::ShowWindow($hwnd, 9)
    if (-not [Win32Drive]::Focus($hwnd)) {
        "warning: could not take the foreground — keystrokes may go elsewhere. Run this from the desktop session."
    }
    Start-Sleep -Seconds 2

    $rect = New-Object Win32Drive+RECT
    [void][Win32Drive]::GetWindowRect($hwnd, [ref]$rect)
    $L = $rect.Left
    $T = $rect.Top

    # --- connect ---------------------------------------------------------
    # Ctrl+T opens Quick connect with the host field focused, then Tab walks the
    # form. No pixel coordinates: those broke every time the sidebar moved.
    [Win32Drive]::Chord([Win32Drive]::VK_CTRL, 0x54)   # 'T'
    Start-Sleep -Milliseconds 900
    Paste-Focused '127.0.0.1'
    [Win32Drive]::Tap(0x09)
    Paste-Focused "$Port"
    [Win32Drive]::Tap(0x09)
    Paste-Focused $User
    [Win32Drive]::Tap(0x09); [Win32Drive]::Tap(0x09); [Win32Drive]::Tap(0x09)   # auth buttons -> password
    Paste-Focused $Password
    Save-Window $hwnd (Join-Path $root 'build\gui-dialog.png')
    [Win32Drive]::Tap(0x0D)                            # Enter submits the form
    Start-Sleep -Seconds 4
    Save-Window $hwnd (Join-Path $root 'build\gui-session.png')

    # --- type in the terminal -------------------------------------------
    [Win32Drive]::Click($L + 700, $T + 400)
    Start-Sleep -Milliseconds 300
    Set-Clipboard -Value 'echo hello-from-the-gui'
    # Ctrl+Shift+V is the app's paste binding; plain Ctrl+V belongs to the shell.
    [Win32Drive]::Chord2([Win32Drive]::VK_CTRL, [Win32Drive]::VK_SHIFT, 0x56)
    Start-Sleep -Milliseconds 400
    [Win32Drive]::Tap(0x0D)                            # Enter
    Start-Sleep -Seconds 2
    Save-Window $hwnd (Join-Path $root 'build\gui-typed.png')

    # --- right-click menu ------------------------------------------------
    Set-Clipboard -Value 'pasted-from-the-menu'
    [Win32Drive]::Click($L + 700, $T + 400, $true)
    Start-Sleep -Milliseconds 800
    Save-Window $hwnd (Join-Path $root 'build\gui-menu.png')

    if ($ClickItem) {
        $parts = $ClickItem.Split(',')
        if ($parts.Count -ne 2) { throw "ClickItem wants 'x,y', got '$ClickItem'" }
        [Win32Drive]::Click($L + [int]$parts[0], $T + [int]$parts[1])
        Start-Sleep -Milliseconds 1200
        Save-Window $hwnd (Join-Path $root 'build\gui-menu-after.png')
        if ($ScreenShot) {
            Start-Sleep -Milliseconds 800
            Save-Screen (Join-Path $root $ScreenShot)
            [Win32Drive]::Tap(0x1B)
            Start-Sleep -Milliseconds 500
        }
    }

    [Win32Drive]::Tap(0x1B)
    "captured dialog/session/typed/menu shots"
}
finally {
    if ($null -ne $script:savedClipboard) { Set-Clipboard -Value $script:savedClipboard -ErrorAction SilentlyContinue }
    if (-not $Keep) {
        if (-not $app.HasExited) { Stop-Process -Id $app.Id -Force -ErrorAction SilentlyContinue }
        Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
        "stopped pid=$($app.Id) and sshtest pid=$($server.Id)"
    }
    Remove-Item Env:\GOTERM_DIR -ErrorAction SilentlyContinue
    Remove-Item Env:\GOTERM_SECRETS -ErrorAction SilentlyContinue
    if (Test-Path $workdir) { Remove-Item -Recurse -Force $workdir -ErrorAction SilentlyContinue }
}
