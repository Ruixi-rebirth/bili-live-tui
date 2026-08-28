# bili-live-tui

在终端中管理 B 站直播：扫码登录、设置直播资料、控制 OBS Studio、收发弹幕、查看推流健康并安全下播。

## 环境要求

- Go 1.26 或更高版本
- OBS Studio 30+（正式推流）
- OBS WebSocket 5.x，默认监听 `127.0.0.1:4455`
- FFmpeg（仅在选择“FFmpeg 测试画面”时需要）

OBS 需要提前配置好场景、画面和声音来源。本程序负责写入 B 站 RTMP 地址并启动/停止推流，不会自动创建摄像头或采集卡来源。

## 运行

```bash
go run ./cmd/bili-live-tui
```

使用 Nix 构建或进入开发环境：

```bash
nix run .
nix build .#bili-live-tui
nix develop
```

也可以直接运行 GitHub 上的版本：

```bash
nix run github:Ruixi-rebirth/bili-live-tui
```

## 界面预览

![弹幕互动页面](docs/screenshots/danmaku.png)

![直播概览](docs/screenshots/overview.png)

首次运行会显示二维码，使用哔哩哔哩手机客户端扫码并确认。登录凭证保存在用户配置目录的 `bili-live-tui/auth.json`。成功开播后，标题、简介、公告、标签、分区、封面、推流方式和方向会保存到同目录的 `live-settings.json`，下次自动回填。

封面支持 JPG、PNG 和 WebP，上传前会自动转换并调整尺寸；标题、简介、公告和标签的字数由 B 站接口校验。
