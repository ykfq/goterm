# GoTerm 2.0 —— mygo 原生 UI 迁移台账

> 本文是 `plan.md` 的迁移对照表：**旧功能 → mygo 实现 / 状态**。
> 迁移完成后，`plan.md` 只保留 1.x（Wails）历史，本文成为 2.x 的主台账。

## 0. 目标与非目标

**目标**

1. 窗口、控件、终端全部由 Go + `github.com/egoist/mygo` 原生绘制，运行时不启动 WebView2，不依赖 Node、npm、Vue、xterm。
2. 保持 GoTerm 1.4.0 的既有能力（连接、认证、标签、终端、SFTP、传输、主机资产、设置、日志）。
3. 三个明确指标：切标签后键盘直接在终端；大输出时内存有硬上限；会话输出可落盘。
4. 产物仍是单个 `goterm.exe`，由 `mygo build` 打包（自带 libghostty-vt）。

**非目标**

- 不保留 Web 前端、Wails 绑定、xterm 插件生态。
- 不为兼容而保留旧的 localStorage 设置（见 §4.9）。
- 不改 SSH 协议栈的行为与红线（`plan.md` 红线 1–8 全部继续生效）。

## 0.1 实施进度（2026-11-10，`mygo-native` 分支）

**已落地（有代码 + 测试）**

- 模块与依赖：Wails/Node/前端目录已删除；`go.mod` 只留 mygo、x/crypto、x/text、sftp、wincred；
  `go tool mygo build` 可产出 `GoTerm.exe` + `ghostty-vt.dll`。
- 原生界面：侧栏（搜索/分组/双击连接/右键编辑删除）、标签栏（状态点/关闭/右键）、设置对话框
  （Appearance/Behaviour/Storage）、主机编辑、Known hosts、确认框、关于框。
- 连接：密码/私钥/agent、跳板机、2FA 提示、口令提示、密码错误重试（上限 3 次）、
  主机密钥 TOFU 与拒绝变更、闲置保活。
- 终端：`terminal.Conn` 直连 SSH 字节流（`internal/sshx/attach.go`），4 MB 有界队列背压。
- **需求 1**：`selectTab` 对每标签 `ui.Handle.Focus()`；`focus_test.go` 用真实 terminal 视图
  验证“点标签 → 键盘在终端”，并做了破坏性验证。
- **需求 2**：队列上限单测（写入阻塞、峰值不超过上限）、VT `Scrollback` 字节上限、无 WebView。
- **需求 3**：`AttachOutput` 与日志共存；`TestAttachedTerminalGetsTheBytesAndTheLogToo`
  用真实 SSH 服务器验证“终端收到的字节 = 日志内容”，且不再发 base64 事件。
- 转码：`internal/encode`（GB18030/Big5/Shift_JIS 流式解码，跨读边界安全）。
- 搜索：终端 `Search/SearchNext/SearchPrevious` + 原生查找条。
- **P1/P2**：主机导入导出（scrypt+AES-GCM，原生保存/打开对话框，往返测试含密码）；
  SFTP 面板（列出/上传/下载/新建/重命名/删除/刷新、路径栏、状态与进度）；
  拖文件到终端即上传；`Ctrl+Shift+E` / 标签右键 / View 菜单开关；`--files` 启动即开。
- **P3**：`transferConn` 把 `trzsz-go` 的 `TrzszFilter` 接在 SSH 字节流上，trzsz 与 ZMODEM 都已启用；
  下载默认落到 `%APPDATA%\goterm\downloads`，上传弹原生文件选择框；关掉了库的 Windows 控制台协议改写。
- **P4 部分**：标签拖拽换位（带活动标签跟随测试）；远端编码切换对已开会话即时生效
  （`decodingReader`，不重建终端）。
- **关键字高亮**：`internal/highlight` 在解码后的字节流上注入 SGR。只处理不含 ESC 的纯文本行；
  跟踪备用屏幕、绝对光标定位和程序自己设置的 SGR，`vim`/`less`/`top`/`htop`、`grep --color`、
  编译器彩色诊断一律不接管。覆盖 `cat`/`tail`/`head`/`ps`/`tee` 与普通日志的关键字、级别、
  IP/URL/时间、shell/bash 命令、syslog 与 nginx 日志/配置；设置里可即时开关。

**尚未落地**

- P4：单词分隔符可配；回看大小对已开终端需重连（终端库没有运行时 setter）。
- 高亮自定义关键字 / `.msyn` 导入（内置规则已覆盖常见纯文本输出；如需自定义再加）。
- P5：automation 端到端脚本、CI 首次全量验证。

**验证记录**

- `go build ./...`、`go vet ./...` 通过。
- `go test .`：焦点两项、SFTP 面板渲染/选中、尺寸与进度格式、导出→删除→导入（含密码）、
  标签换位、trzsz 真实下载（sshtest 的 TrzszShell，57 KB 字节目录一致）均通过。
- `go test ./internal/sshx`（输出队列/背压/日志共存）通过。
- `go test ./internal/highlight`：纯文本着色且字节不变、程序颜色优先、备用屏幕/绝对光标不接管、
  跨块 ESC、二进制与超长行直通、关闭后完全透传、Reader 小缓冲不丢字节。
- ZMODEM 两个方向都已自动化：`internal/sshtest/lrzsz` 提供远端 `rz`/`sz`，`TestLrzszUpload` 与 `TestLrzszDownload` 都在真实 SSH 上通过。
- 真机：`--connect --files` 连本地 sshtest（SFTP 根目录预置 `logs/`、`readme.txt`），
  面板正确列出条目，终端与面板并排 68 列；截图只留在本地。
- 真机：`--connect` 连本地 sshtest，窗口显示终端、直接发送按键即到达会话；截图只留在本地。
- 打包：`go tool mygo build -platform windows/amd64` 产出 `GoTerm.exe` + `ghostty-vt.dll`；
  用该产物连 sshtest，日志文件 `logs/goterm-<时间>-tester_127.0.0.1_2222.log` 正常写入。

## 1. 架构对照

| 1.x（Wails） | 2.x（mygo） |
| --- | --- |
| `main.go` + `app.go` 服务绑定 | `main.go` 启动 `mygo.App`，`app.go` 是普通 Go 控制器 |
| Vue 组件 + `store.ts` | `ui` 包视图函数（`ui.Column/Row/List/Modal/...`） |
| `xterm.js` + addons | `plugins/terminal`（Ghostty libghostty-vt） |
| Go→JS 事件 + base64 字节管道 | `terminal.Options.Conn` 直接读会话字节流 |
| 前端流控（4MB 暂停 / 1MB 恢复） | `io.Reader` 背压（队列 4MB，满了 SSH 读阻塞） |
| localStorage 设置 | `%APPDATA%\goterm\settings.json` |
| CSS 主题 + xterm 调色板 | mygo 主题 + `terminal.Theme`（Ghostty 主题） |
| vitest 451 例 | Go `testing` + `ui.NewTester` + automation 插件 |

## 2. 功能平替台账

状态：✅ 已平替（本分支已有代码）· 🚧 本轮实施 · ⏸️ 后续阶段 · ⚠️ 需要决策 · ❌ 不再适用

### 2.1 连接、认证与保活（plan.md §2）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| 密码 / 私钥 / ssh-agent 三种认证 | `internal/sshx` 原样复用 | ✅ |
| 没存密码 → 拨号前先问 | `hoststore.ErrPasswordRequired` → 原生对话框 → 重试 | 🚧 |
| 私钥口令弹窗、立即重试 | `sshx.ErrPassphraseRequired` → 原生对话框 | 🚧 |
| 只有勾选才落密钥环 | `SetHostSecret` + 对话框"记住"开关 | 🚧 |
| 跳板机口令单独一条 | `ConnectOverrides.ProxyPassword`，`(proxy)` 错误后缀 | 🚧 |
| 跳板机 + 加密私钥弹窗 | 同上，`ProxyPassphrase` | 🚧 |
| 2FA（动态口令追加） | 原生单字段对话框，仅密码认证 | 🚧 |
| 主机密钥 TOFU / 不一致拒连 / forget | `HostKeyStore` 不变；新增 Known hosts 面板 | 🚧 |
| 半开连接 / 握手超时 | `internal/sshx` 不变 | ✅ |
| 保活不发回车（红线 1） | 不变 | ✅ |
| 连接级闲置窗口、anti-idle | 不变 | ✅ |

### 2.2 渲染、配色与高亮（plan.md §3）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| 二进制保真 | 字节直接进 VT，无 base64 往返 | ✅（更短路径） |
| GPU 渲染 | mygo D3D11/Metal/OpenGL，VT 自绘 | ✅ |
| 关键字高亮（MobaXterm filter） | `internal/highlight`：只装饰“不含 ESC 的纯文本行”，跟踪备用屏幕/绝对光标/程序 SGR；`cat`/`tail`/`head`/`ps`/`tee` 与普通日志着色，`vim`/`less`/`top` 等全屏程序不接管 | ✅ |
| `grep --color` 不覆盖 | 程序自己设置了前景/背景/反显时，该行不再注入颜色；粗体/下划线仍可叠加 | ✅ |
| 自定义关键字 / `.msyn` 导入 | 内置 shell/bash/syslog/nginx/data/words 规则；自定义规则尚未暴露到界面 | 🚧 |
| 字体只列等宽 | `internal/fonts` 不变，`ui.RegisterFont`/`terminal.Font` | ✅ |
| 单词分隔符可配 | Ghostty 选择规则固定 | ⚠️ |
| 自定义背景色 / 主题 | `terminal.Theme` + Ghostty 主题列表 | 🚧 |
| htop/systemctl 撑不满 | VT 自带 reflow + resize | ✅ |
| 块字符粘连 | Ghostty 自绘块/框线，无需开关 | ✅（旧开关取消） |
| 中文乱码 → GB18030/Big5/Shift_JIS | Go 侧流式转码（`transform.NewReader`），设置改动对已开会话即时生效 | ✅ |

### 2.3 键盘与鼠标（plan.md §4）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| Ctrl+W/K/T/B 不被应用抢走 | 应用快捷键只用自己的组合；终端优先 | ✅ |
| **点标签后键盘就在终端里** | 每个标签持 `ui.Handle`，切标签即 `Focus()`（控件未显示时排队，显示后生效） | 🚧 |
| 应用命令 Ctrl+Shift+… | mygo 菜单加速键 | 🚧 |
| 快捷键不向会话多发字符 | 菜单/Shortcut 先于终端消费 | ✅ |
| 选中即复制 | `terminal.CopyOnSelect` | 🚧 |
| 多行粘贴确认 | `Options.OnPaste` + 原生确认框 | 🚧 |
| 终端 paste 接管 | 原生剪贴板，`OnPaste` | ✅ |
| TUI 右键归程序，Shift+右键本地菜单 | Ghostty 默认行为 | ✅ |
| OSC 52 复制（只写不读） | `NoClipboardWrite=false` + `ClipboardRead=Deny` | ✅ |
| Shift+拖选复制 | Ghostty 选择模型 | ✅ |
| Ctrl+滚轮缩放 | 终端内置 | ✅ |
| Ctrl+Shift+F 搜索 | `Terminal.Search/SearchNext/SearchPrevious` + `ui.FindBar` | 🚧 |

### 2.4 标签与会话（plan.md §5）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| 多标签、每标签独立会话 | `[]*Tab`，每标签一个 `terminal.Terminal` | 🚧 |
| Duplicate 不二次登录 | `Manager.Duplicate` 不变 | 🚧 |
| 连接引用计数 / 最后标签才关 | `internal/sshx/link.go` 不变 | ✅ |
| Reconnect | 关终端 → 用原 Config 重连 | 🚧 |
| ad-hoc 会话 Reconnect 置灰 | 无 hostID 时禁用菜单项 | 🚧 |
| 拖拽调整标签顺序 | 自定义标签栏 + `Dragged()` 换位 | 🚧 |
| **大输出内存涨到 1G** | 终端滚动回看有字节上限；Conn 队列 4MB 满即停读 SSH（TCP 背压） | 🚧 |
| 滚动回看可设 | 改为 **MB**（默认 8 MB，1–64），因为 Ghostty 按字节计 | 🚧 |
| 断线后 `session is closed` 刷屏 | 状态栏/标签状态单一载体，不往终端写 | 🚧 |
| 断开条不被 toast 遮挡 | 原生布局，断开条固定在终端上方 | 🚧 |

### 2.5 文件传输（plan.md §6）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| trzsz（trz/tsz）拦截 + 进度 | `transferConn` 用 `trzsz-go` 的 `TrzszFilter` 包住 SSH 字节流；进度条由过滤器画进终端 | ✅ |
| ZMODEM `rz`/`sz` | `internal/zmodem` 纯 Go 实现（hex/bin 头、CRC16、ZFILE/ZDATA/ZEOF、ZRINIT/ZRPOS 握手），方向在首帧判定，`sz` 下载先等远端宣告、`rz` 上传改由本机发起并按对端应答切方向；真实 SSH 下两个方向都已通过（`TestLrzszUpload`、`TestLrzszDownload`），传输期终端暂不渲染、进度写回屏显队列 | ✅ |
| 拖文件进终端即上传 | `ui.DroppedFiles` + SFTP 上传 | ✅ |
| 原生文件选择框 | mygo 桌面 API（`mygo.Dialog`） | ✅ |
| 路径限制 / `../` 消毒 | `internal/sftpx` 不变 | ✅ |
| 取消不报错 | 原生对话框取消语义 | ✅ |
| SFTP 面板 | `internal/sftpx` + 原生列表 | ✅ |
| 传输中不插字节（红线 4） | 传输期间终端字节由过滤器独占，UI 不插手 | ✅ |

### 2.6 界面、主题与设置（plan.md §7）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| 侧栏主机列表（搜索/分组/双击连接） | `ui.SearchField` + `ui.Sidebar` | 🚧 |
| 深/浅色主题 | mygo 跟随系统 + `terminal.Theme` | 🚧 |
| 标题栏跟随主题 | 原生窗口，系统处理 | ✅ |
| 设置面板分页 | `ui.Tabs` + `ui.Modal` | 🚧 |
| 块字符开关 | 取消（Ghostty 自绘） | ❌ |
| 远端编码 | 设置 → 编码，Go 转码（即时生效） | ✅ |
| 状态栏（版本/平台/凭据后端） | 底部状态栏 | 🚧 |
| 不啰嗦的 toast | `c.Toast` 只在必要时 | 🚧 |
| 设置即时生效并持久化 | `settings.json`，终端 `SetFont/SetTheme` | 🚧 |
| Scrollback / Session log 设置 | 见 §2.4 | 🚧 |
| 状态目录可改 | `internal/appdir` 不变，原生目录选择 | ✅（下次启动生效） |

### 2.7 主机资产、凭据与安全（plan.md §8）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| `hosts.json` 不含密码 | 不变 | ✅ |
| Windows 凭据管理器 / 密封文件 | 不变 | ✅ |
| 导入导出（scrypt+AES-GCM） | 不变；原生保存/打开对话框 | ✅ |
| 导入幂等、缺密码不清本地 | `hoststore.Import` 不变 | ✅ |
| `.ppk` / `.pub` 明确报错 | 不变 | ✅ |
| 密码永不进 UI 进程 | 原生 UI 与 Go 同进程，仍不落盘、不写日志 | ✅ |

### 2.8 输入法与本地化（plan.md §9）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| TUI 里输入法红叉 | WebView2 特有；终端插件用系统 IME 在光标处组字 | ✅（架构性消除，待真机确认） |
| 切标签后 Shift+Alt 切不回中文 | 同上 | ✅（待真机确认） |

### 2.9 工程、测试与打包（plan.md §10）

| 事项 | 2.x 做法 | 状态 |
| --- | --- | --- |
| Go 测试（SSH 栈等 8 包） | 保留 | ✅ |
| 前端 451 例 | 删除，改为 Go + `ui.NewTester` | 🚧 |
| GUI 冒烟脚本 | 改为 automation 插件（`plugins/automation`） | ⏸️ P5 |
| 发布流水线 | `mygo build -platform windows/amd64` + 上传 Release | ⏸️ P5 |
| NSIS 安装包（旧 ❌） | `mygo build` 自带 Windows installer，可补上 | ⏸️ P5 |
| 仓库卫生 | 不提交 `.toolchain/`、`bin/`、`out/` | ✅ |

## 3. 关键机制（实现约定）

### 3.1 终端焦点（需求 1）

- 每个 `Tab` 持有 `ui.Handle`；终端元素 `.Bind(&tab.Focus)`。
- 切标签、重连完成、窗口重新获得键盘时调用 `tab.Focus.Focus()`。
- 只有"用户显式切换标签"才抢焦点；设置/对话框关闭后优先还焦点给终端。
- 验收：自动化脚本点标签 → 打字 → 会话收到字节；`ui.NewTester` 覆盖 `Focused()` 状态。

### 3.2 内存上限（需求 2）

三层限制，缺一不可：

1. **VT 滚动回看**：`terminal.Options.Scrollback` 字节上限（默认 8 MB）。
2. **管道背压**：SSH 输出 → 4 MB 有界队列 → 终端 `Read`；队列满时读取 goroutine 阻塞，SSH 窗口关闭，远端停止发送。
3. **无 WebView**：不再有 Chromium 堆、JS 字符串、IPC 队列的重复拷贝。

验收：向会话灌 200 MB 输出，进程工作集增长不超过 队列 + scrollback + 常量；`runtime.ReadMemStats` 断言在 `attach_test.go`。

### 3.3 日志（需求 3）

- 复用 `internal/sshx/sessionlog.go`：`%APPDATA%\goterm\logs\goterm-<时间>-<目标>.log`。
- 记录的是**服务器原始字节**（转码前），与终端显示内容在 UTF-8 模式下逐字节一致。
- 开关对已开会话即时生效；默认关闭；路径显示在状态栏/设置里。

### 3.4 转码

- `internal/encode` 提供 `utf-8`（直通）、`gb18030`、`big5`、`shift_jis`。
- 用 `transform.NewReader` 包在会话字节流上，跨 read 边界安全；非法序列替换为 U+FFFD，不吞字节。
- 输入始终按 UTF-8 发送（与 1.x 相同）。

## 4. 分阶段计划

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| **P0 骨架**（本轮） | 模块换 mygo、原生窗口、侧栏 + 标签 + 终端、密码/口令/OTP 提示、焦点、背压、日志、基础设置 | `go build` + `go test` 全绿；真实 SSH 会话可输入、可滚动、日志落盘 |
| P1 主机完整性 | 主机编辑/删除/分组、跳板机、Known hosts 面板、导入导出、原生文件对话框 | 主机 CRUD 与 1.x 等价 |
| P2 SFTP | 面板、上传/下载、拖拽上传、路径安全 | 与 1.x SFTP 行为一致 |
| P3 传输 | trzsz/tsz、rz/sz（Go 实现）、进度条、输入闸门 | 大文件 md5 一致、无乱码 |
| P4 体验对齐 | 搜索条、标签拖拽排序、右键菜单、快捷键、状态栏、编码切换、字体列表 | 与 1.x 操作路径逐项对照 |
| P5 工程收尾 | `ui.NewTester` 测试、automation E2E、`mygo build` 发布流水线、README/plan 更新 | tag 触发 Actions 产出 exe + 校验和 |

## 5. 验证方式

```powershell
go test ./...
go vet ./...
gofmt -l .
go tool mygo build          # 或 wails 替代品：mygo build
```

- 纯逻辑：Go 单元测试（SSH、存储、设置、转码、背压）。
- 视图：`ui.NewTester` 点击/输入/断言文本与焦点。
- 真机：启动 exe，连接本地 `sshtest`，切标签打字、`cat` 大文件看内存、检查日志文件。

## 6. 需要决策/说明的差异

1. **关键字高亮**：VT 模拟器没有逐行改色接口，因此在字节流层注入 SGR，但把边界写死在实现里：
   只处理不含 ESC 的纯文本行；进入备用屏幕（`?1049`/`?1047`/`?47`）、出现绝对光标定位（`H`/`f`）、
   或程序自己选了颜色时，高亮自动让位。因此 `cat`/`tail`/`head`/`ps`/`tee`/`top -b` 会着色，
   而 `vim`/`less`/交互式 `top` 保持原样。自定义规则与 `.msyn` 导入仍待做。
2. **滚动回看单位**：Ghostty 按字节，不再按行；默认 8 MB。
3. **设置迁移**：旧 localStorage 设置不迁移，升级后回默认值。
4. **trzsz/ZMODEM**：已接 `trzsz-go` 的 `TrzszFilter`（`transfer.go`），关掉了它的 Windows 控制台协议改写（我们不是 ConPTY），
   下载默认落到 `%APPDATA%\goterm\downloads`；上传弹原生文件选择框。
5. **调试代理/本机路径**：一律不进仓库；`.toolchain/`、`out/`、`bin/` 在 `.gitignore`。
