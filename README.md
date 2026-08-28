<div align="center">

# 📺 bili-live-tui

**面向 Bilibili 主播的轻量级终端直播控制台**

_突破 5000 粉丝推流限制，让 Linux / macOS / Windows 都能自由使用 OBS 推流。集开播配置、自动联动、实时弹幕与房管治理于一体。_

<br>

[![Release](https://img.shields.io/github/v/release/Ruixi-rebirth/bili-live-tui?style=flat-square&color=23ade5&label=Release)](https://github.com/Ruixi-rebirth/bili-live-tui/releases)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![OBS Studio](<https://img.shields.io/badge/OBS%20Studio-30+%20(ws%205.x)-302F38?style=flat-square&logo=obsstudio&logoColor=white>)](https://obsproject.com/)
[![Platforms](https://img.shields.io/badge/Platform-Linux%20|%20macOS%20|%20Windows-blue.svg?style=flat-square)](https://github.com/Ruixi-rebirth/bili-live-tui/releases)
[![Nix](https://img.shields.io/badge/Nix-Flake%20Ready-5277C3?style=flat-square&logo=nixos&logoColor=white)](https://nixos.org/)
[![License](https://img.shields.io/badge/License-MIT-brightgreen.svg?style=flat-square)](LICENSE)

<br>

[⚡ 快速上手](#-快速上手-30-秒开播) • [💡 设计初衷](#-设计初衷) • [✨ 功能概览](#-功能概览) • [🎮 OBS 联动配置](#-obs-联动配置) • [⌨️ 快捷键速查](#️-全键盘快捷键速查) • [🔒 隐私与安全](#-隐私与安全保障)

</div>

---

## 💡 设计初衷

在 B 站进行日常直播时，许多个人与中小主播都会面临一个极不友好的规则限制：**粉丝不足 5000 时，官方网页直播中心不开放第三方 RTMP 推流码，强制要求使用官方的“哔哩哔哩直播姬”**。

然而，**官方直播姬仅提供 Windows 版本**。这意味着：

- **Linux 与 macOS 用户在粉丝不足 5000 时，几乎无法在电脑上正常开播**。
- 即使在 Windows 下，主播也被迫绑定在封闭臃肿的官方客户端中，无法自由使用跨平台、插件生态成熟的 OBS Studio。
- 即使后续满足了推流资格，日常开播也往往繁琐割裂：每次都要在网页上手动复制推流地址与密钥到 OBS、挂着浏览器看弹幕、需要禁言时再去后台层层翻找菜单。

`bili-live-tui` 的初衷就是打破这一死锁，将主播最常用的操作整合进一个轻量、跨平台、纯键盘驱动的终端界面中：

- **突破 5000 粉丝推流限制**：借助移动端签名协议，**无需任何粉丝量门槛**，开播即可直接获取 B 站官方 RTMP 推流地址与推流密钥。彻底解开 Linux / macOS 用户的开播死锁，让所有系统的主播都能自由使用 OBS Studio 或 FFmpeg 进行推流。
- **OBS 自动化联动**：开播时通过 OBS WebSocket 自动同步推流地址并开启推流，下播自动停止，无需每次手动复制粘贴推流码。
- **跨平台与轻量低占用**：原生 Go 编写，运行常驻内存仅数十 MB，Linux / macOS / Windows 全平台通用，无需在后台常驻多标签页浏览器或臃肿客户端。
- **一站式闭环工作流**：开播配置、弹幕收发、观众画像、房管与全局禁言、推流状态监控及本地画面回看，均在一个工作区中完成。
- **本地与安全保障**：直连 B 站官方接口与本地 OBS，凭据存储在本地私有文件（`0600` 权限），直播推流码仅在内存中流转，绝不落盘。

---

## ✨ 功能概览

### 1. 开播配置与 OBS 自动推流联动

借助移动端签名协议，**无需 5000 粉丝门槛**即可直接获取 B 站官方 RTMP 推流地址与推流码。启动即自动拉取并回填上次成功的开播信息（标题、二级分类、标签、公告、简介与封面）。选择输出模式为 OBS 后，只需敲击回车，程序会自动向 B 站申请开播、通过 WebSocket 将推流地址与推流码写入 OBS 并控制开启推流。

<p align="center">
  <img src="docs/screenshots/settings.png" alt="开播信息配置界面" width="880">
  <br>
  <em>▲ 开播信息配置：支持多级分区选择、本地封面自适应压缩上传与推流模式设定</em>
</p>

- 🖼️ **封面自动上传**：直接输入本地 JPG、PNG、WebP 封面路径，自动转换为 B 站规范尺寸并压缩上传。
- 🧪 **FFmpeg 链路测试**：未配置 OBS 时，也可一键启动 FFmpeg 测试画面验证完整直播推流链路。

---

### 2. 实时弹幕与观众互动卡片

采用底层长连接 WebSocket 协议，实时解析并呈现普通弹幕、大航海（舰长/提督/总督）进房、高能榜变动、礼物赠送与醒目留言。

<p align="center">
  <img src="docs/screenshots/danmaku.png" alt="实时弹幕互动与观众画像卡片" width="880">
  <br>
  <em>▲ 实时弹幕流、右侧高能榜与一键呼出的用户画像/房管操作弹窗</em>
</p>

- ⌨️ **终端快速回复**：直接在底部输入框输入文字，按下 `Enter` 即可发送弹幕到直播间。
- 👤 **观众资料卡片**：使用键盘或鼠标点击任意发言观众，呼出资料卡（展示勋章、大航海级别、UID、公开主页数据等），并支持直接 `@TA`、关注/取关、警告、禁言、拉黑或任免房管。
- 🚨 **超管警告拦截**：实时捕获 B 站官方超管整改警告与切断通知，第一时间弹窗高亮提示，避免直播间异常封禁。

---

### 3. 房间管理工作区

终端内集成的房间管理后台，方便在开播过程中快速处理房管任免、用户禁言与全局发言控制。

<p align="center">
  <img src="docs/screenshots/room_manager.png" alt="房间管理工作区" width="880">
  <br>
  <em>▲ 房间管理中心：房管任免、禁言名单、多阶时长封禁、黑名单与全局禁言配置</em>
</p>

- 🛡️ **房管列表与任免**：查看当前房管与容量上限，支持普通房管与高级房管的任命与撤销。
- ⏱️ **灵活禁言管理**：查看实时禁言名单，支持按 UID 或用户名查找用户，可选「本次直播 / 2小时 / 4小时 / 1天 / 7天 / 永久禁言」等多种惩罚周期，支持随时一键解禁。
- 🚫 **黑名单治理**：查看房间黑名单，支持通过用户名或 UID 搜索快速拉黑与移出。
- 🔒 **多模式全局禁言**：突发节奏时，支持开启全员禁言、仅粉丝发言、荣耀等级门槛、粉丝勋章门槛或仅房管发言模式。

---

### 4. 直播看板与推流监控

在弹幕界面随时切换至「直播概览」看板，查看直播间状态与推流指标。

<p align="center">
  <img src="docs/screenshots/overview.png" alt="直播概览与推流监控看板" width="880">
  <br>
  <em>▲ 直播概览看板：推流健康指标、在线人气、免下播修改房间资料与一键 MPV 回看</em>
</p>

- 📈 **推流健康监控**：实时展示推流码率 (Bitrate)、输出帧率 (FPS)、掉帧统计（帧数及丢帧率）与 OBS 进程 CPU 占用。
- 🔄 **免下播修改资料**：直播中途可在看板点击「修改资料」，在线更新标题、分区和公告，无需关播重启。
- 🎬 **mpv 画面回看**：点击「预览直播」调用本地 `mpv` 播放器，采用零缓存低延迟配置拉取观众端真实画面，默认静音避免麦克风回授。
- 🔄 **开播状态接管**：若因终端意外关闭或网络中断导致程序退出，再次启动程序时会自动检测到开播状态，并提供「接管直播」选项直接恢复弹幕与管理。

---

## ⚡ 快速上手 (30 秒开播)

### 方式 1：Nix 一键免安装运行（推荐）

如果你是 Nix / NixOS 用户，无需手动配置 Go 环境和依赖，一条命令直接运行：

```bash
nix run github:Ruixi-rebirth/bili-live-tui
```

### 方式 2：下载预编译二进制（解压即用）

从 [GitHub Releases](https://github.com/Ruixi-rebirth/bili-live-tui/releases) 下载对应系统的预编译压缩包，解压后运行即可：

| 操作系统    | 架构类型              | 预编译下载文件                                                                                       |
| :---------- | :-------------------- | :--------------------------------------------------------------------------------------------------- |
| **Linux**   | x86_64 (amd64)        | [`bili-live-tui_linux_amd64.tar.gz`](https://github.com/Ruixi-rebirth/bili-live-tui/releases/latest) |
| **Linux**   | AArch64 (arm64)       | [`bili-live-tui_linux_arm64.tar.gz`](https://github.com/Ruixi-rebirth/bili-live-tui/releases/latest) |
| **macOS**   | Apple Silicon (M系列) | [`bili-live-tui_macos_arm64.tar.gz`](https://github.com/Ruixi-rebirth/bili-live-tui/releases/latest) |
| **macOS**   | Intel x86_64          | [`bili-live-tui_macos_amd64.tar.gz`](https://github.com/Ruixi-rebirth/bili-live-tui/releases/latest) |
| **Windows** | 64 位 (amd64)         | [`bili-live-tui_windows_amd64.zip`](https://github.com/Ruixi-rebirth/bili-live-tui/releases/latest)  |

### 方式 3：从源码安装与编译

要求本地环境安装有 **Go 1.26+**：

```bash
# 一键安装到 $GOPATH/bin
go install github.com/Ruixi-rebirth/bili-live-tui/cmd/bili-live-tui@latest

# 或克隆仓库后本地运行
git clone https://github.com/Ruixi-rebirth/bili-live-tui.git
cd bili-live-tui
go run ./cmd/bili-live-tui
```

### 📱 登录流程

首次启动时，终端会直接渲染安全的登录二维码。打开**哔哩哔哩手机客户端**扫描并确认登录即可。登录成功后，安全凭证将保存在本地配置目录，后续启动自动免扫码登录。

---

## 🎮 OBS 极简联动配置

`bili-live-tui` 原生支持 **OBS Studio 30+** 的 WebSocket 5.x 协议，只需 3 步即可实现全自动推流：

1. **打开 OBS Studio**，在顶部菜单栏点击 `工具 (Tools)` ➔ `WebSocket 服务器设置 (WebSocket Server Settings)`。
2. **勾选「启用 WebSocket 服务器」**，服务器端口保持默认的 `4455`。
3. （可选）如果启用了身份验证并设置了密码，在 `bili-live-tui` 开播界面填入对应密码即可。

> [!TIP]
> 提前在 OBS 中配置好你的游戏捕获、摄像头、麦克风和画面场景。开播时，`bili-live-tui` 会自动写入 B 站分配的 RTMP 地址与推流密钥并触发推流，绝不会修改你精心布置的场景元素。若本机 OBS 尚未开启，程序还会尝试自动唤起本地已安装的 OBS 进程。

---

## ⌨️ 全键盘快捷键速查

`bili-live-tui` 支持全键盘盲操与现代终端鼠标点击，操作行云流水：

### 📋 开播配置界面

| 快捷键              | 功能说明                                     |
| :------------------ | :------------------------------------------- |
| `Tab` / `Shift+Tab` | 在表单各输入项与底部按钮之间切换焦点         |
| `Enter`             | 确认当前输入项；在「开始直播」按钮上触发开播 |
| `Ctrl+U`            | 快速清空当前聚焦输入框的内容                 |
| `←` / `→`           | 在底部的「开始直播」与「取消开播」按钮间切换 |
| `Esc` / `Ctrl+C`    | 取消本次开播并退出程序                       |

### 💬 弹幕互动工作区

| 快捷键              | 功能说明                                                                           |
| :------------------ | :--------------------------------------------------------------------------------- |
| `Tab` / `Shift+Tab` | 在 **弹幕输入框 ⇄ 直播概览按钮 ⇄ 房间管理按钮 ⇄ 弹幕流 ⇄ 高能榜** 之间循环轮转焦点 |
| `Enter`             | （输入框聚焦时）立即发送弹幕                                                       |
| `←` / `→`           | （高能榜聚焦时）在高能用户榜与大航海舰队榜之间切换                                 |
| `Ctrl+U`            | 一键清空已输入的弹幕草稿                                                           |
| `Ctrl+L`            | 清空屏幕已渲染的历史弹幕（清屏）                                                   |
| `Esc`               | 唤出下播确认对话框（可选取消或确认下播）                                           |
| `Ctrl+C`            | 快速请求安全下播并退出                                                             |

### 👤 观众卡片弹窗

| 快捷键                | 功能说明                                                             |
| :-------------------- | :------------------------------------------------------------------- |
| `↑` / `↓` / `←` / `→` | 在卡片底部各房管操作按钮间移动                                       |
| `Enter`               | 执行选中的房管操作（`@TA`、关注/取关、警告、禁言、拉黑、设为房管等） |
| `Esc`                 | 关闭用户画像卡片，焦点返回弹幕框                                     |

### 🛡️ 房间管理工作区

| 快捷键              | 功能说明                                                                              |
| :------------------ | :------------------------------------------------------------------------------------ |
| `↑` / `↓`           | （在左侧栏目列表中）切换 **房管 / 禁言 / 黑名单 / 全局禁言** 栏目；在表格中选择数据行 |
| `Tab` / `Shift+Tab` | 在 **栏目列表 ⇄ 内容表格 ⇄ 操作按钮栏** 之间轮转焦点                                  |
| `[` 或 `PageUp`     | 列表上一页翻页                                                                        |
| `]` 或 `PageDown`   | 列表下一页翻页                                                                        |
| `Enter`             | 触发当前选中的表格操作或底部按钮                                                      |
| `Esc`               | 退出房间管理，返回弹幕互动主界面                                                      |

---

## 💡 命令行实用选项

除了交互式 TUI 界面外，`bili-live-tui` 还提供了便捷的命令行参数，适合与系统脚本或快捷方式结合使用：

```bash
# 查看当前直播间的开播状态与推流状态（支持 -status 或 status）
bili-live-tui -status

# 一键下播：向 B 站发送下播请求并结束推流（支持 -stop 或 stop，适合直播中断或应急快速关播）
bili-live-tui -stop

# 禁用色彩样式，使用终端原生纯色配色
bili-live-tui --no-color
# 或通过标准环境变量
NO_COLOR=1 bili-live-tui
```

---

## 🔒 隐私与安全保障

- **零中转，官方直连**：程序直接与 Bilibili 官方 API 及本地 OBS WebSocket 进行通信，没有任何第三方中间服务器，绝不收集或上传任何主播数据。
- **本地凭证高安全存储**：登录会话凭据保存在系统配置目录下的 `bili-live-tui/auth.json`，开播历史保存在 `live-settings.json`。应用目录权限设为 `0700`，文件权限严格限制为 `0600`（仅当前操作系统用户可读写），并采用原子写入防止崩溃截断。
- **推流密钥永不落盘**：B 站直播分配的敏感推流码（Stream Key）仅在运行时存放于系统内存中，并通过本地回环网络直接注入 OBS，**绝不以明文或文件形式保存在磁盘上**，从根源上杜绝推流码意外泄漏的风险。

---

## 🛠️ 开发与测试

欢迎参与 `bili-live-tui` 的改进与建设！

```bash
# 运行单元测试
go test ./...

# 运行静态代码检查
go vet ./...
```

对于 Nix 用户：

```bash
# 进入配置完善的开发环境（包含 Go 1.26、gopls、gotools）
nix develop

# 构建 Flake 包
nix build .#bili-live-tui
```

---

## 📄 开源许可证

本项目采用 [MIT License](LICENSE) 开源许可证，可自由学习、使用与扩展。
