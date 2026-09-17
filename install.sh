#!/bin/sh
# Relay 的 POSIX 安装器。下载物必须通过校验后才可替换或执行。
set -eu

fail() { printf 'relay: %s\n' "$*" >&2; exit 1; }
mode=${RELAY_DOWNLOAD_MODE:-direct}
case "$mode" in
    direct) command -v curl >/dev/null 2>&1 || fail '请先安装 curl' ;;
    gh) command -v gh >/dev/null 2>&1 || fail '私有发行需要已登录的 GitHub CLI (gh)' ;;
    *) fail 'RELAY_DOWNLOAD_MODE 只支持 direct 或 gh' ;;
esac
fetch() {
    if [ "$mode" = gh ]; then
        gh release download "$version" --repo "$repo" --pattern "${1##*/}" --output "$2"
    else
        curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fLsS --retry 3 --connect-timeout 15 --max-time 300 "$1" -o "$2"
    fi
}
command -v tar >/dev/null 2>&1 || fail '请先安装 tar'
if command -v sha256sum >/dev/null 2>&1; then
    checksum() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
    checksum() { shasum -a 256 "$1" | awk '{print $1}'; }
else
    fail '需要 sha256sum 或 shasum 才能校验下载物'
fi

case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) fail '仅支持 Linux 和 macOS' ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) fail '仅支持 amd64 和 arm64' ;; esac
repo=${RELAY_REPOSITORY:-bingame/cli-relay}
case "$repo" in *[!a-zA-Z0-9_./-]*|''|*..*) fail '无效的 RELAY_REPOSITORY' ;; esac
version=${RELAY_VERSION:-latest}
if [ "$version" = latest ]; then
    if [ "$mode" = gh ]; then
        version=$(gh api "repos/$repo/releases/latest" --jq .tag_name) || fail '无法读取私有发行；请确认 gh 已登录且有仓库访问权限'
    else
        resolved=$(curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fLsS --retry 3 --connect-timeout 15 --max-time 60 -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") || fail '无法获取最新版本；私有仓库请设置 RELAY_DOWNLOAD_MODE=gh 并登录 gh'
        version=${resolved##*/}
    fi
fi
case "$version" in v[0-9]*) ;; *) fail '版本必须为 v 开头的发布 tag，例如 v0.1.0' ;; esac
case "$version" in *[!a-zA-Z0-9.+-]*) fail '无效的版本号' ;; esac
base="https://github.com/$repo/releases/download/$version"
asset="relay-$os-$arch.tar.gz"
work=$(mktemp -d) || fail '无法创建临时目录'
stage=
cleanup() { rm -f "${stage:-$work/unused}"; rm -rf "$work"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
fetch "$base/checksums.txt" "$work/checksums.txt"
fetch "$base/$asset" "$work/$asset"
expected=$(awk -v name="$asset" '$2 == name {print $1}' "$work/checksums.txt")
[ "${#expected}" -eq 64 ] || fail '校验文件缺少唯一的 SHA-256 条目'
case "$expected" in *[!0-9a-fA-F]*) fail 'SHA-256 格式不合法' ;; esac
actual=$(checksum "$work/$asset")
[ "$(printf '%s' "$expected" | tr 'A-F' 'a-f')" = "$actual" ] || fail 'SHA-256 校验失败，未修改现有安装'
tar -xzf "$work/$asset" -C "$work" relay
if [ ! -f "$work/relay" ] || [ -L "$work/relay" ]; then
    fail '发行包中缺少普通 relay 文件'
fi
chmod 755 "$work/relay"

dest=${RELAY_INSTALL_DIR:-"$HOME/.local/bin"}
case "$dest" in /*) ;; *) fail 'RELAY_INSTALL_DIR 必须为绝对路径' ;; esac
if ! mkdir -p "$dest" 2>/dev/null || [ ! -w "$dest" ]; then
    [ -z "${RELAY_INSTALL_DIR:-}" ] || fail '指定的安装目录不可写'
    dest=/usr/local/bin
fi
if [ -w "$dest" ]; then
    [ ! -L "$dest/relay" ] || fail '目标 relay 是符号链接，请先检查现有安装'
    stage=$(mktemp "$dest/.relay-install.XXXXXX")
    cp "$work/relay" "$stage"
    chmod 755 "$stage"
    mv -f "$stage" "$dest/relay"
    stage=
else
    command -v sudo >/dev/null 2>&1 || fail '用户目录不可写，且无法通过 sudo 安装到 /usr/local/bin'
    printf '用户目录不可写；需要 sudo 将 Relay 安装到 /usr/local/bin。\n' >&2
    # /dev/tty 防止 curl | sh 把脚本内容当作密码输入。
    # shellcheck disable=SC2024
    sudo -v </dev/tty || fail '未获得 sudo 权限'
    sudo mkdir -p "$dest"
    sudo test ! -L "$dest/relay" || fail '目标 relay 是符号链接'
    privileged_stage=$(sudo mktemp "$dest/.relay-install.XXXXXX")
    if ! sudo install -m 755 "$work/relay" "$privileged_stage" || ! sudo mv -f "$privileged_stage" "$dest/relay"; then
        sudo rm -f "$privileged_stage"
        fail '写入系统安装目录失败'
    fi
fi

# 只持久化目录，不复制整个当前 PATH；单引号转义支持含空格的 HOME。
quoted=$(printf '%s' "$dest" | sed "s/'/'\\\\''/g")
path_line="export PATH='$quoted':\"\$PATH\""
case ":$PATH:" in *":$dest:"*) ;; *)
    if [ "${RELAY_NO_MODIFY_PATH:-0}" != 1 ]; then
        profile="$HOME/.profile"
        case "${SHELL:-}" in */zsh) profile="$HOME/.zshrc" ;; */bash) profile="$HOME/.bashrc" ;; esac
        if ! grep -Fqx "$path_line" "$profile" 2>/dev/null; then
            printf '\n# Relay\n%s\n' "$path_line" >> "$profile"
        fi
    fi
    printf '当前终端执行以下命令即可使用 relay（或重新打开终端）：\n%s\n' "$path_line"
    ;;
esac
PATH="$dest:$PATH"
export PATH
"$dest/relay" --version
if [ "${RELAY_SKIP_SKILLS:-0}" != 1 ]; then
    "$dest/relay" skill install || fail '二进制已安装，但 Skill 安装失败；处理上述问题后运行 relay skill install'
fi
printf 'Relay 已安装到 %s/relay。运行 relay --help 开始使用。\n' "$dest"
