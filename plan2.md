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
- **下载去向两个协议问同一句话**（2.2）：`tsz` 与 `sz` 都弹同一个原生目录选择框。
  `sz` 在 ZMODEM 握手前问；`tsz` 由 `transferConn.Write` 问——**按住那一下回车**，
  等答案出来再决定要不要把命令送出去。反过来在字节流里按住 `::TRZSZ:TRANSFER:` 试过，
  trzsz-go 的握手在部分时序下会崩（`zlib: invalid header`），所以一个字节的协议流都不碰。
  用户说"不"就只吞掉回车，命令还留在提示符上没执行。
  只认一行的第一个词：`cd /tmp && tsz x` 与 ↑ 调出的历史命令不弹框，落回默认目录。
- **取消 lrzsz 不再卡住会话**（2.2）：目录框被取消时，之前只是本地返回错误，
  远端 `sz` 还在等那个永远不来的 ZRINIT，它的 shell 排在后面，终端又是 raw 模式，
  于是 Ctrl+C 与回车都进了协议读——只能关标签。现在取消即发 `zmodem.Cancel()`
  （ZABORT + ZFIN），并有 2 秒静默窗口，不把对端的收尾帧当成新一次传输。
  `TestLrzszCancelReleasesTheRemote` 用真 SSH 验证：屏幕出现 `canceled`，再敲一行能得到提示符响应。
- **rz 上传此前根本发不出去**（2.2）：对着真 lrzsz 的语义逐条纠正——
  binary 帧少了每帧都有的 4 字节 offset（对端会把文件头 4 个字节当成偏移），
  二进制头也没有自己那枚校验和（对端会把数据头两字节当成校验和，判为坏帧）；
  ZFILE 描述符写死 `0 0`（等于告诉 `rz` 这是空文件）、ZEOF 偏移写 0；
  窗口用 ZCRCG 结尾却等一个真 `rz` 永远不会发的 ZACK——选完文件就此不动，
  正是用户看到的"没反应"。现在 offset、header 校验和、真实大小、ZCRCE→ZEOF→ZRINIT
  的顺序、ZCRCQ 窗口回 ZCRCR、ZRPOS 重传都对齐 lrzsz；CRC 覆盖的是**转义前**的字节
  （lrzsz 在决定转义之前就把字节喂进校验和）。
- **镜像测试证明不了互操作**（2.2）：`internal/sshtest/lrzsz` 原来的 `rz` 用的就是我们自己的
  `zmodem.Receiver`，两边自说自话。新增 `rzstrict`：按真 `rz` 的脾气来的接收端——核对描述符里的
  大小、核对每帧 offset、每个 ZCRCQ 必须等到 ZCRCR 才继续、ZEOF 的数量必须与描述符一致。
  `TestLrzszUploadSurvivesARigidPeer` 用 114688 字节（四个窗口）打过去；改之前它连一字节都收不到。
- **sz 下载侧同步补上**：接收端回 ZCRCR（不然对端发完一个窗口就停住）、按帧里的 offset 落位、
  ZEOF 数量与收到的字节不符直接报错，不再悄悄存下一个短文件。
- **目录框的按钮**：Windows 的目录选择框写着"打开"，用户会以为不能选。现在显式给
  `ButtonLabel`（"Choose This Folder"）；空选择不再静默当作取消，状态栏会说 `no folder chosen`。
- **P4 部分**：标签拖拽换位（带活动标签跟随测试）；远端编码切换对已开会话即时生效
  （`decodingReader`，不重建终端）。
- **关键字高亮**：`internal/highlight` 在解码后的字节流上注入 SGR。只处理不含 ESC 的纯文本行；
  跟踪备用屏幕、绝对光标定位和程序自己设置的 SGR，`vim`/`less`/`top`/`htop`、`grep --color`、
  编译器彩色诊断一律不接管。覆盖 `cat`/`tail`/`head`/`ps`/`tee` 与普通日志的关键字、级别、
  IP/URL/时间、shell/bash 命令、syslog 与 nginx 日志/配置；设置里可即时开关。

**新增（2026-11-12，开发环境迁到 Linux）**

- **保活制度修正**（JumpServer 60 分钟断线）：连接级探针默认间隔 45s→**15s**，探测超时独立成 5s；
- **按键保活默认开启 + 时序修正**（2.2.0 真机仍被踢）：旧文件缺 `version` 时迁移成开并重写；预约时间改为
  **上次按键 + 窗口**（输出完全不参与，否则一直打印的程序会让键击饿死），抖动只提前不推后；`pause` 改成
  单个定时器而不是 100ms 轮询（轮询掩盖了"唤醒时按 now 重排、节奏翻倍"的 bug）。
  `TestNudgeNeverLandsLaterThanTheWindow`、`TestIdleKeystrokeDefeatsAUserActivityPolicy`、
  `TestAFreshStartArmsTheIdleKeystroke`、`TestContinuousOutputDoesNotPostponeTheKeystroke`、
  `TestNudgeSurvivesALoneEscape` 钉住；
  停发条件从「键盘闲置到窗口」改为「闲置到窗口 **且** 窗口不可见（`Manager.SetDetached`，
  由 `OnFocus/OnBlur/IsMinimized` 上报）」。可见即继续防御，离开才放弃租约（红线 8 仍然成立）。
  决策与证据见 `docs/DECISIONS.md`「Keepalives」一节。
- **应用图标**：`resources/icon.png`（1024²）由 `resources/goterm.icon.jpg` 紧贴墨水裁切
  （2% 安全边）生成，未重绘；mygo 把它打进 exe，`icon.go` 同步 embed、`Open()` 用它设窗口图标。
  新版构图就是 "G + 终端徽章"（1106×716），方形图标里 G 占比足够，所以之前为 GT 字标加的
  单字母小图（`icon-small.png`）已删除，一套图各尺寸通用。
  `resourcesExclude: ["*.jpg"]` 让设计稿只进二进制、不随 exe 发布。
- **自定义高亮关键字**：`settings.json` 的 `highlightWords`（≤64 条，单条 ≤64B，去重限幅），
  设置 → 行为 里逐行编辑，**即时作用到已开会话**（`SetWords` 走和 `SetHighlight` 同一条
  接口探测路径）。ASCII 词只整词匹配（ERR 不打错 error 的半个词），含非 ASCII（日文/中文等）
  按子串匹配；不覆盖内置规则与程序自己的颜色。
- **单行顶栏**：隐藏系统标题栏（`TitleBarHidden`）与菜单栏（`AutoHideMenuBar`，Alt/F10 唤出、
  快捷键照常），视图自绘 30px 一行：方形徽标（点开与 Alt/F10 相同的菜单）+ "GoTerm"（拖动窗口）
  + 标签 + 新建按钮，平台窗口按钮由 mygo 画在右侧。左侧留白与侧栏同宽，标签从侧栏右缘开始；
  折叠后对齐窄轨。菜单由 `menu.go` 的一张表同时生成原生菜单栏与徽标菜单。
  徽标 22px（原先 16px 在 30px 行里几乎看不清，16px 标题栏是真痛点）。
- **侧栏折叠修复**：折叠后 `main` 列缺 `FillHeight`，列按内容收缩，标签栏与状态栏被拉进
  窗芯；现在外层 Row `Grow+Fill+Stretch`、内层 Column `Grow+FillHeight`。
  `TestCollapsedSidebarKeepsChromeAtTheEdges` 钉住（回退到旧布局时状态栏实测落在 y=462/800）。
- **Linux 开发环境**：`build/test.sh`（vet + 全测试）；Go 1.27 工具链 +
  `https://mirrors.tencent.com/go` 写在用户级 `go env`，不入库。
  **Linux 已知问题（非本项目）**：mygo 的 libghostty-vt 与 pango 在同一进程里会段错误
  （Ghostty 静态链了自己的排版库）。`test.sh` 用 `MYGO_GHOSTTY_VT=/nonexistent`
  让终端引擎类测试在 Linux 上确定性 SKIP（与库缺失时同一出口）；Windows/macOS 不受影响，
  终端插件由 CI 在 Windows 上验证。

**尚未落地**

- P4：单词分隔符可配；回看大小对已开终端需重连（终端库没有运行时 setter）。
- 高亮 `.msyn` 导入（自定义关键字已落地；`.msyn` 文件解析仍未做，内置 + 自定义已覆盖日常）。
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
- （2026-11-12）bastion 制度：`sshtest` 新增 `HangUpAfterSilence` 模拟堡垒机静默断连，
  `TestJumpHostKeepsAQuietSessionAlive` / `TestQuietTargetKeepsItsSession` 在无一次按键的
  三个窗口后仍 connected 且 PTY 零增量；对照面 `watch()` 证明策略确实会杀（关保活必死），
  `TestAKeepaliveSlowerThanTheBastionLosesTheSession` 证明慢于窗口的间隔救不回来。
  idle 全套改写为双条件（可见不停、离开才停），`go test ./internal/sshx` 全绿。
- （2026-11-12）Linux：`./build/test.sh` 全绿（Go 1.27.4 + 腾讯镜像）；
  交叉编译产出可跑的 `GoTerm.exe` 已验证。
- （2026-11-12）自定义关键字：`internal/highlight/words_test.go`（大小写、整词、CJK 子串、
  不压内置、长词优先、中途换表即生效）+ settings 解析/限幅/落盘往返测试。

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
| 2FA（动态口令追加） | 原生单字段对话框，仅密码认证；缺码有独立哨兵 `ErrTwoFactorRequired`，**重连清空旧码并重新弹窗**；输入框只收数字、上限 8 位 | ✅ |
| 主机密钥 TOFU / 不一致拒连 / forget | `HostKeyStore` 不变；新增 Known hosts 面板 | 🚧 |
| 半开连接 / 握手超时 | `internal/sshx` 不变 | ✅ |
| SSH 保活（keepalive@openssh.com） | `link.go`：默认 **每 15 秒** 一次（`DefaultKeepAlive`）；闲置且不可见才停发。这一层守的是 NAT/负载均衡/sshd `ClientAliveInterval` 这类**连接级**几十秒的空闲预算，故意不跟按键保活的 2 分钟同频 |
| **闲置按键保活（2.2，默认开）** | 连接级探针不碰 shell，`TMOUT` 与"看用户活动"的堡垒机都看不见它。开启后：**行是空的 → 发一个回车**（空行回车只换提示符、不执行任何命令）；**行上有未提交内容 → 只发一个空格**（不可能提交；真机反馈"空格+退格"在目标机上不能重置计时器，所以就用这一个键）。**默认每 1 分钟**、不因输出而推迟、传输中完全不发、键击不计入用户活动时钟（红线 8）；行上"不是空行"的任何状态（半条命令、按键翻出的历史命令、半个转义序列）一律发空格——旧版遇到单独一个 ESC 会永久静默，那是"功能开着却掉线"的一条路。设置项带开关/间隔/回退。**默认开**：关着的时候用户只是被静默踢下线，没有任何界面提示需要去拨一个开关（2.2.0 的 settings.json 都写着 `"idleNudge": false`，那是旧默认而不是选择，由 `version` 迁移改一次并重写文件）。状态栏常显 `idle key 60s · N sent · next Xs`，把"开关是开的"和"真有字节发出去了"分开 |
| **保活真机失效的三条根因（2.2.0 第五轮）** | ① 每个标签都挂着传输过滤器，而过滤器创建时就把会话标成"传输中"，**永不复位** → 键击在真机上一条都发不出去（测试里的会话没有过滤器，所以一直是绿的）。现在标记跟着 trzsz 的 `SetTransferStateCallback` 和 ZMODEM 的对端走，传输结束立刻归还。② 连接级探针连续 3 次没回音就**主动关闭会话**——堡垒机可以不回复它不认识的全局请求，这个"静默"不能当死亡证明；现在只有传输层报错才算死。③ 窗口失焦被当成"人走了"，闲置窗口一过就停探测、停键击（JumpServer 一小时断开就是这么来的）；现在只有最小化才算。附：`logs/keepalive.log` 记录每次连接参数、每次键击（含被抑制的原因）、每次探针无应答和每次断开原因，1 MB 轮转 | ✅ |
| ~~红线 1「保活绝不发回车」~~ | **用户 2026-11-13 明确改为例外**：仅当行上没有任何未提交字符时才发回车，空格用于有输入时。见 `docs/DECISIONS.md` §B-2 |

### 2.2 渲染、配色与高亮（plan.md §3）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| 二进制保真 | 字节直接进 VT，无 base64 往返 | ✅（更短路径） |
| GPU 渲染 | mygo D3D11/Metal/OpenGL，VT 自绘 | ✅ |
| 关键字高亮（MobaXterm filter） | `internal/highlight`：只装饰“不含 ESC 的纯文本行”，跟踪备用屏幕/绝对光标/程序 SGR；`cat`/`tail`/`head`/`ps`/`tee` 与普通日志着色，`vim`/`less`/`top` 等全屏程序不接管 | ✅ |
| `grep --color` 不覆盖 | 程序自己设置了前景/背景/反显时，该行不再注入颜色；粗体/下划线仍可叠加 | ✅ |
| 自定义关键字 / `.msyn` 导入 | 内置 shell/bash/syslog/nginx/data/words 规则；自定义关键字走 `highlightWords`（设置页逐行编辑，即时生效，ASCII 整词 / 非 ASCII 子串）；`.msyn` 导入未做 | ✅ |
| 字体只列等宽 | `internal/fonts` 不变，`ui.RegisterFont`/`terminal.Font` | ✅ |
| 单词分隔符可配 | Ghostty 选择规则固定 | ⚠️ |
| 自定义背景色 / 主题 | `terminal.Theme` + Ghostty 主题列表 | 🚧 |
| htop/systemctl 撑不满 | VT 自带 reflow + resize | ✅ |
| 块字符粘连 | Ghostty 自绘块/框线，无需开关 | ✅（旧开关取消） |
| 光标形状 | `terminal.Options.Cursor = CursorBar`：竖线而不是方块（方块在 14px 下压住相邻字符，看起来像反色单元格）；程序仍可用 DECSCUSR 改 | ✅ |
| 中文乱码 → GB18030/Big5/Shift_JIS | Go 侧流式转码（`transform.NewReader`），设置改动对已开会话即时生效 | ✅ |

### 2.3 键盘与鼠标（plan.md §4）

| 需求 | 2.x 做法 | 状态 |
| --- | --- | --- |
| Ctrl+W/K/T/B 不被应用抢走 | 应用快捷键只用自己的组合；终端优先 | ✅ |
| **点标签后键盘就在终端里** | 每个标签持 `ui.Handle`，切标签即 `Focus()`（控件未显示时排队，显示后生效） | 🚧 |
| 应用命令 Ctrl+Shift+… | mygo 菜单加速键 | 🚧 |
| 快捷键不向会话多发字符 | 菜单/Shortcut 先于终端消费 | ✅ |
| 选中即复制 | `terminal.CopyOnSelect` | 🚧 |
| 多行粘贴确认 | `Options.OnPaste`（`paste.go`）：先剥终端控制序列（SGR/CSI/OSC/DCS，连同被截断的半个序列）；多行弹**可编辑的多行文本框**——里面就是将要发送的全文（旧版只给首行预览，等于让用户批准三行却只看见一行），勾选项「逐行去掉行尾空格 + 去掉末尾空行」默认开启（末尾空行会被 shell 当成一条命令跑掉；行尾空格会混进命令里）；发送前把 CRLF/LF 统一成**单个 CR**——否则每个换行被终端读成两次回车，每两行之间多跑一条空命令；**Paste 是默认按钮，回车即粘贴**（按钮持有键盘；文本框故意不抢焦点，因为在那里回车必须是换行）；干净单行走终端自己的快速路径 | ✅ |
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

Linux 开发机上一条命令替代前三行：`./build/test.sh`（内含终端引擎的 pango 冲突规避，
见 §0.1 的说明）。

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
