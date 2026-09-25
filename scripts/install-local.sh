#!/bin/sh
# 从当前仓库源码构建 Relay，并覆盖本机已安装的二进制。
#
# 与 install.sh 的区别：install.sh 下载 Release 上的预编译产物，不含本地改动；
# 本脚本构建当前工作区代码，供改完代码后立即装到本机测试时使用。
#
# 用法：
#   sh scripts/install-local.sh [--test] [--skip-skill] [--no-backup] [--install-dir DIR]
#
# 环境变量（与 install.sh 一致）：
#   RELAY_INSTALL_DIR  目标安装目录，默认 ~/.local/bin
set -eu

ensure_block() {
    file=$1
    start=$2
    end=$3
    block=$4
    if [ -e "$file" ] && [ -L "$file" ]; then
        return 1
    fi
    if [ ! -e "$file" ]; then
        if ! mkdir -p "$(dirname "$file")" || ! printf '%s\n' "$block" > "$file"; then
            return 1
        fi
        return 0
    fi
    tmp=$(mktemp) || return 1
    awk -v s="$start" -v e="$end" '
        $0 == s { skip=1; next }
        $0 == e { skip=0; next }
        skip == 0 { print }
    ' "$file" > "$tmp" || { rm -f "$tmp"; return 1; }
    if [ -s "$tmp" ]; then
        printf '\n%s\n' "$block" >> "$tmp"
    else
        printf '%s\n' "$block" > "$tmp"
    fi
    if cat "$tmp" > "$file"; then
        rm -f "$tmp"
        return 0
    fi
    rm -f "$tmp"
    return 1
}

install_completion() {
    relay_path=$1
    completion_installed=0
    [ "${RELAY_NO_COMPLETION:-0}" = 1 ] && return 0
    start='# RELAY:START completion'
    end='# RELAY:END completion'
    case "${SHELL:-}" in
        */zsh) shell=zsh ;;
        */fish) shell=fish ;;
        *) shell=bash ;;
    esac
    quoted=$(printf '%s' "$relay_path" | sed "s/'/'\\\\''/g")
    if [ "$shell" = fish ]; then
        fish_dir=${XDG_CONFIG_HOME:-"$HOME/.config"}/fish/completions
        fish_file="$fish_dir/relay.fish"
        if [ -e "$fish_file" ] && [ -L "$fish_file" ]; then
            printf 'relay: %s 是符号链接，未修改补全。\n' "$fish_file" >&2
            return 0
        fi
        if [ -f "$fish_file" ] && ! grep -qF "$start" "$fish_file"; then
            printf 'relay: %s 已存在且没有 Relay 标记，保留原文件；补全不会自动更新。\n' "$fish_file" >&2
            return 0
        fi
        fish_script=$("$relay_path" completion fish) || {
            printf 'relay: 无法生成 fish 补全。\n' >&2
            return 0
        }
        case "$fish_script" in
            *'complete -c relay'*) ;;
            *) printf 'relay: 生成的 fish 补全无效，未写入。\n' >&2; return 0 ;;
        esac
        mkdir -p "$fish_dir"
        if ! ensure_block "$fish_file" "$start" "$end" "$fish_script"; then
            printf 'relay: 写入 %s 失败。\n' "$fish_file" >&2
            return 0
        fi
        completion_installed=1
        return 0
    fi
    completion_output=$("$relay_path" completion "$shell") || {
        printf 'relay: 无法生成 %s 补全。\n' "$shell" >&2
        return 0
    }
    case "$completion_output" in
        *_relay*) ;;
        *) printf 'relay: 生成的 %s 补全无效，未写入。\n' "$shell" >&2; return 0 ;;
    esac
    if [ "$shell" = zsh ]; then
        profile=${ZDOTDIR:-"$HOME"}/.zshrc
        block="$(printf '%s\n' \
            "$start" \
            'if typeset -f compdef >/dev/null 2>&1; then' \
            '    :' \
            'else' \
            '    compinit -C' \
            'fi' \
            "eval \"\$('$quoted' completion zsh)\"" \
            "$end")"
    else
        profile="$HOME/.bashrc"
        block="$(printf '%s\n' \
            "$start" \
            "eval \"\$('$quoted' completion bash)\"" \
            "$end")"
    fi
    if ! ensure_block "$profile" "$start" "$end" "$block"; then
        printf 'relay: 写入 %s 失败。\n' "$profile" >&2
        return 0
    fi
    completion_installed=1
    return 0
}

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
install_dir=${RELAY_INSTALL_DIR:-}
run_tests=0
skip_skill=0
no_backup=0

while [ $# -gt 0 ]; do
    case $1 in
        --test) run_tests=1 ;;
        --skip-skill) skip_skill=1 ;;
        --no-backup) no_backup=1 ;;
        --install-dir)
            [ $# -ge 2 ] || { echo 'install-local: --install-dir 需要一个参数' >&2; exit 1; }
            install_dir=$2
            shift
            ;;
        *)
            echo "install-local: 未知参数 $1" >&2
            exit 1
            ;;
    esac
    shift
done

[ -f "$repo_root/go.mod" ] || { echo "install-local: 未在 $repo_root 找到 go.mod" >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo 'install-local: 未找到 go 命令；Relay 需要 Go 1.26 或更新版本' >&2; exit 1; }

if [ -z "$install_dir" ]; then
    [ -n "${HOME:-}" ] || { echo 'install-local: HOME 未设置，请用 --install-dir 指定目录' >&2; exit 1; }
    install_dir=$HOME/.local/bin
fi
case $install_dir in
    /*) ;;
    *) echo 'install-local: --install-dir 必须是绝对路径' >&2; exit 1 ;;
esac
target=$install_dir/relay

# 版本串只用于 relay --version 显示；GoReleaser 才会注入正式 tag。
revision=$(git -C "$repo_root" rev-parse --short HEAD 2>/dev/null || echo unknown)
version="v0.0.0-local+$revision"
if [ -n "$(git -C "$repo_root" status --porcelain 2>/dev/null)" ]; then
    version="$version.dirty"
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/relay-local-XXXXXX")
stage=
cleanup() {
    [ -z "$stage" ] || rm -f -- "$stage"
    rm -rf -- "$work"
}
trap cleanup EXIT INT TERM

if [ "$run_tests" -eq 1 ]; then
    echo 'go test ./...'
    (cd "$repo_root" && go test ./...)
fi

echo "go build ($version)"
ldflags="-X github.com/bingame/cli-relay/internal/version.Version=$version"
(cd "$repo_root" && CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$work/relay" ./cmd/relay)

# 先放到安装目录内的暂存名，再同目录 rename，失败时旧二进制不受影响。
mkdir -p -- "$install_dir"
stage=$install_dir/.relay-install-$$.tmp
cp -- "$work/relay" "$stage"
if [ "$no_backup" -eq 1 ]; then
    mv -f -- "$stage" "$target"
else
    [ ! -f "$target" ] || cp -- "$target" "$target.bak"
    mv -f -- "$stage" "$target"
fi
stage=

"$target" --version
if [ "$skip_skill" -eq 0 ]; then
    "$target" skill install
fi
install_completion "$target"

echo "已安装 $version 到 $target"
if [ "$completion_installed" -eq 1 ]; then
    echo "新终端会自动加载 relay 命令补全。"
fi
[ "$no_backup" -eq 1 ] || [ ! -f "$target.bak" ] || echo "回滚：mv -f '$target.bak' '$target'"
