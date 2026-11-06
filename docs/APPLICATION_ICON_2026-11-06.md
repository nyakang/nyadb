# 应用图标更换与验证 — 2026-11-06

## 资源与接线

用户提供的 `temp/nyadb-logo-transparent.svg` 原样保存为
`internal/assets/appicon/icon.svg`，不改变路径、原色 `#05E6C1` 或透明背景，
不另行指定许可证。来源及重建方法见 [资源说明](../internal/assets/appicon/README.md)。

生成脚本使用 ImageMagick 7.1.2-18，将 SVG 高分辨率渲染、裁去透明留白，
等比缩放至最长边 840 像素并居中放入 1024×1024 RGBA 画布。
实际非透明边界为 `[130, 92, 894, 932]`；不增加底板、描边或阴影。

`mygo.json` 显式引用生成的 PNG；`appicon.PNG()` 将同一份 PNG 嵌入应用并返回
独立字节副本。bootstrap 在创建主窗口之前调用 `App.Dock.SetIcon`，设置失败
仅记录诊断。连接编辑器保留现有数据库图标和默认恢复行为。旧
`resources/icon.png` 占位资源已删除。普通 Go/MyGo 构建不需要生成工具。

## 检查结果

环境为 Linux amd64、Go 1.27.3，以及 go.mod 固定的 MyGo `v0.7.4-nyadb.1`。

| 检查 | 结果 |
| --- | --- |
| SVG 原文件逐字节比较、PNG 尺寸/透明度/居中及不透明像素原色 | 通过 |
| 从其他工作目录重建 PNG | 字节一致 |
| 嵌入 PNG 与打包源一致、返回字节副本相互独立 | 通过 |
| 资源包和 bootstrap 测试、现有连接窗口生命周期/图标回归 | 通过 |
| appicon/bootstrap Go vet、MyGo UI vet、Python 语法检查 | 通过 |
| Linux amd64、Windows amd64、macOS arm64 MyGo 编译和打包 | 通过；未生成 Windows 安装器或 macOS DMG，未执行 macOS 签名 |
| Linux 桌面入口、tar.gz 原图及 deb 内 256/512 像素图标 | 通过 |
| Windows PE 内七组 PNG 图标资源 | 16/24/32/48/64/128/256 像素，均带透明通道 |
| macOS Info.plist 与 ICNS | 正确关联，32–1024 像素；最大尺寸的可见像素及透明度与原图一致 |

产物和检查记录位于忽略的 `build/appicon-verification/`。打包命令为：

```sh
GOWORK=off CGO_ENABLED=0 go tool mygo build \
  -o build/appicon-verification/package \
  -platform linux/amd64,windows/amd64,darwin/arm64 \
  -skip-installer -skip-dmg -skip-notarize
```

## Linux 原生窗口

使用各自独立的 Xvfb/Openbox 和临时配置，实际操作主窗口、新建连接选择器、
MySQL 编辑器及“上一步”返回选择器，不连接数据库。系统密钥服务在该隔离
环境中不可用，不影响图标检查。

以下三组最终运行均完整完成并正常退出：

- `native-go-run-final/`：从仓库目录执行 `go run ./cmd/nyadb`。
- `native-dev-external-final/`：从其他工作目录启动普通 Go 构建的开发二进制。
- `native-package-final/`：从其他工作目录启动 MyGo 打包的 Linux 正式程序。

默认图标通过 X11 WM_HINTS 位图读取；选择器及返回后的图标与主窗口位图
一致。MySQL 编辑器的 `_NET_WM_ICON` 与原 64×64 数据库 PNG 逐像素一致。
各运行的 `events.json`、截图和 `cleanup.json` 保留在对应目录，探测过程的
非最终运行记录也保留。已检查 16/24/32/48/256 像素的深浅背景预览：
[尺寸预览](../build/appicon-verification/sizes.png)。

Windows/macOS 的任务栏、Finder、Dock 和实体桌面显示尚未实机验证；
Linux Xvfb 窗口不代表 Wayland、多显示器缩放或所有桌面环境。
