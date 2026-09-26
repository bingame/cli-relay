# 发布与安装实现说明

## 上游版本

固定 GoReleaser **v2.9.0**。2026-09-17 实测其 `check` 和五平台 snapshot 均通过。v2.18.2 已将 `brews` 标为弃用，`check` 因此失败；替代的 Homebrew Cask 只面向 macOS。本项目需要 Linux/macOS 共用 formula，暂固定最后一批支持 formula 的稳定版本，不使用浮动 `latest`。升级 GoReleaser 时必须同时迁移 formula 生成方式并重新验收。

`archives.formats: binary` 的 snapshot 产物在 `artifacts.json` 中映射到构建目录的原始文件，发布时使用 `name_template` 对应的名称。因此 Windows 安装链接确实为 `relay-windows-amd64.exe`，而本地快照内不一定有同名根目录文件。

## 公开上线检查

远端为 `bingame/cli-relay` 时，公开仓库和公开 Release 可直接使用 README 中的 `curl` / `irm` 安装命令，不需要 `gh`。安装器不读取或输出 GitHub token。公开前应在 GitHub 仓库设置中确认：默认分支保护已启用、版本 tag 仅允许受信任维护者创建、Secret scanning 与 push protection 已开启。

1. 启用 GitHub Actions；先通过 `验证` 工作流，随后推送语义版本 tag（例如 `v0.1.0`）。`发布` 工作流依赖三系统测试和五平台打包验证通过，再发布 Release。
2. 创建 Homebrew tap 与 Scoop bucket。默认分别为 `bingame/homebrew-tap`、`bingame/scoop-bucket`。`PACKAGE_REPO_TOKEN` 只授予这两个软件源仓库的 contents 写权限，通过 GitHub Actions Secret 提供。
3. 如采用需求中的 `brew install relay-cli/tap/relay`，设置仓库 Variables：`HOMEBREW_TAP_OWNER=relay-cli`、`HOMEBREW_TAP_REPO=homebrew-tap`，并先创建该仓库。Scoop 可用 `SCOOP_BUCKET_OWNER` / `SCOOP_BUCKET_REPO` 自定义。首次须 `scoop bucket add relay https://github.com/<owner>/<bucket>`，之后才可 `scoop install relay`；不会假设已进入 Scoop 官方 bucket。
4. 没有 `PACKAGE_REPO_TOKEN` 时，发布仍生成 formula 和 manifest，作为 Release 附件交付，但不向软件源提交。预发布版本也不推进正式软件源。
5. 推荐顺序：先在仍为私有的仓库提交并通过 CI，再创建一个包含安装与卸载脚本的正式 Release，验证 Release 附件后切换仓库为 Public。这样公开后的 README 链接立即可用，也不会出现公开仓库指向尚未发布的卸载脚本。

## 安装行为

- 默认选择最新稳定 Release，也可用 `RELAY_VERSION=v0.1.0` 固定版本。下载二进制前先把 latest 解析为具体 tag，避免并发发布时混用版本。仅允许 HTTPS 下载。
- `RELAY_DOWNLOAD_MODE=direct` 为默认公开 HTTPS 下载，`gh` 模式通过已登录 GitHub CLI 读取和下载私有 Release；授权失败直接报错，不自动尝试其他凭据。Homebrew/Scoop 的默认资源 URL 为公开地址，私有部署先使用 gh 安装器，不把软件源清单生成视为匿名软件源已可用。
- POSIX 默认创建 `~/.local/bin`，不可写才回退 `/usr/local/bin` 并显示 sudo 提示；可通过 `RELAY_INSTALL_DIR` 指定绝对目录。SHA-256 校验后解包并同目录原子替换。用户 shell 为 bash/zsh 时持久化到对应 rc 文件，否则写 `.profile`。`curl | sh` 无法改变父 shell 的环境，当前终端需要执行输出的 `export`，或重新打开终端。
- Windows 默认 `%LOCALAPPDATA%\Relay\bin`，SHA-256 校验后同卷替换，更新当前进程及用户 PATH。不调用 `setx`，避免 PATH 截断、展开或混入系统 PATH。只发布 amd64，不把其他架构误报成 amd64。
- 从私有镜像安装时可使用 `RELAY_DOWNLOAD_MODE=gh`。PowerShell 命令必须使用 `gh release download --output - | Out-String | iex`：`gh` 的标准输出进入 PowerShell 后是多个逐行字符串对象；缺少 `Out-String` 时，`iex` 会逐项执行，在多行函数闭合前报语法错误。`install.ps1` 同时只包含 ASCII 字符，避免原生程序输出按其他系统代码页解码 UTF-8 中文时破坏脚本。测试会拒绝重新引入非 ASCII 字节，并以逐行读取、聚合后执行的方式验证安装器。
- 两端默认执行新安装二进制的 `skill install`。失败会返回非零并指明二进制已经安装，不能把 Skill 失败当成完整成功。`RELAY_SKIP_SKILLS=1` 可跳过；`RELAY_NO_MODIFY_PATH=1` 不持久化 PATH。
- 两端默认还会把 `relay` 命令补全写入当前用户 shell 配置：PowerShell 写 profile，bash/zsh 写对应 rc，fish 写独立 `relay.fish`，重开终端自动生效。单独运行 `relay completion powershell` 只打印补全脚本，不会注册。
- `RELAY_NO_COMPLETION=1` 跳过补全，PATH 与 Skill 安装照常。PowerShell profile 保留原编码与换行，任一 profile 是符号链接则整体失败；bash/zsh 使用绝对路径 `eval`，zsh 只在缺少 compdef 时运行 `compinit -C`；fish 已存在且无托管标记的 `relay.fish` 会被保留并提示，符号链接拒绝修改。
- 手动归档和 `go install` 没有安装后钩子，需要执行一次 `relay skill install`。Homebrew/Scoop 带安装后钩子。
- Release 同时附带 `uninstall.sh` 和 `uninstall.ps1`。卸载脚本删除二进制、托管补全和 Relay 安装的 Skill，但默认保留 Relay 数据目录与加密凭据，避免误删供应商配置。
- SHA-256 能检测下载损坏和不匹配，信任来源为 HTTPS 发行仓库；当前没有独立签名、macOS 公证、apt/deb 或 winget 发布流程。

## 验证

`scripts/test_installers.py` 使用真实二进制、临时目录和离线下载替身；验证首次安装、自动 Skill、重复升级、PowerShell 安装器纯 ASCII 及逐行管道聚合、校验不匹配/重复条目/下载失败时保留旧文件、用户修改保护，以及命令补全写入、重复安装幂等、跳过安装、符号链接保护和 `pwsh` 下 `CompleteInput` 的注册探测。Windows 测试禁用用户 PATH 持久化；POSIX PATH 只写临时 HOME，覆盖含空格及单引号的路径。不会修改真实 Claude/Codex/Multica 配置。

`scripts/check_release.py dist` 根据 GoReleaser 的 artifact 元数据校验五平台归档、Windows 裸二进制及 Homebrew/Scoop 校验和。CI 额外在 Linux、macOS、Windows 运行原生构建及安装器；Linux 运行 race detector 和 ShellCheck。
