# GoTerm

**纯 Go 实现的桌面 SSH 终端**：界面由 [mygo](https://mygo.egoist.dev) 原生绘制，终端模拟器来自
[Ghostty](https://ghostty.org)（libghostty-vt，运行时加载）。

没有 WebView2、没有 Chromium、没有 Node、没有前端构建：窗口、标签、设置、终端全部是 Go。也正因为如此，
会话狂刷输出时内存仍有硬上限（4 MB 输出队列 + 8 MB 回看，之后由 SSH 背压停读）。

[English](README.md) · **简体中文** · [迁移台账](plan2.md) · [1.x 台账](plan.md)

---

## 状态：2.0 预览

本分支（`mygo-native`）替换 1.4.0 的 Wails + Vue + xterm 前端。目前可用：

- SSH 密码 / 私钥 / ssh-agent 认证；跳板机；双因子动态口令；主机密钥首次信任（变更即拒连）；
  保活与闲置窗口。
- 原生主机列表（分组、搜索）、主机编辑（地址、端口、用户、认证、跳板机、2FA）。
- 多标签：复制标签不二次登录、重连、关闭。
- **切换标签后键盘直接落在终端里**，不用再点一次终端。
- 真终端：颜色、鼠标上报、回看、选择、复制粘贴、搜索（Ctrl+Shift+F）、输入法在光标处组字、
  OSC 52 复制。
- 会话日志写入 `%APPDATA%\goterm\logs`（默认关闭，运行时开关即时生效）。
- 设置存 `settings.json`：主题、字体与字号、回看（MB）、日志、远端编码
  （UTF-8/GB18030/Big5/Shift_JIS）、选中即复制、闲置小时数、数据目录。

尚未迁移（见 [plan2.md](plan2.md)）：trzsz/ZMODEM 传输、SFTP 面板与拖拽上传、主机导入导出、
关键字高亮（VT 模拟器没有逐行着色钩子）、标签拖拽排序、Known hosts 的复核（目前只能列出与遗忘）。

## 构建

要求：Go 1.27.3 或更高。不需要别的。

```powershell
go build -o bin\goterm.exe .
.\bin\goterm.exe
```

不经过 mygo CLI 构建的程序会在首次运行时把 `libghostty-vt` 下载到
`%LOCALAPPDATA%\mygo\natives` 并校验哈希。要生成发布用的包：

```powershell
go tool mygo build -platform windows/amd64 -o out -skip-installer
# out\windows-amd64\GoTerm.exe + ghostty-vt.dll
```

发布流水线会把这两个文件、许可证与校验和打成 zip。

## 使用

- **新建主机**（Ctrl+Shift+T），填地址并选择认证方式，然后在侧栏双击主机连接。
- 终端内搜索：Ctrl+Shift+F；关闭标签：Ctrl+Shift+W；设置：Ctrl+,。
- 标签右键：Duplicate（复制）、Reconnect（重连）。
- `goterm.exe --connect <主机ID>` 启动时直接连接已保存的主机。

密码存放在 Windows 凭据管理器（没有密钥环的平台回退密封文件），永远不会写进 `hosts.json`。
会话日志记录的是服务器原始字节（解码前），与 SSH 实际送达的内容一致。

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)。
