#!/bin/sh
set -eu

fail() {
    printf 'relay uninstall: %s\n' "$1" >&2
    exit 1
}

remove_block() {
    file=$1
    start=$2
    end=$3
    [ -f "$file" ] || return 0
    [ -L "$file" ] && {
        printf 'relay uninstall: leaving symlink %s unchanged\n' "$file" >&2
        return 0
    }
    tmp=$(mktemp "$file.relay-uninstall.XXXXXX") || fail "cannot create temporary file for $file"
    if ! awk -v start="$start" -v end="$end" '
        $0 == start { skipping = 1; next }
        skipping && $0 == end { skipping = 0; next }
        !skipping { print }
        END { if (skipping) exit 2 }
    ' "$file" > "$tmp"; then
        rm -f "$tmp"
        fail "cannot update $file"
    fi
    if cmp -s "$file" "$tmp"; then
        rm -f "$tmp"
    else
        mv -f "$tmp" "$file"
    fi
}

remove_path_line() {
    file=$1
    path_line=$2
    [ -f "$file" ] || return 0
    [ -L "$file" ] && return 0
    tmp=$(mktemp "$file.relay-uninstall.XXXXXX") || fail "cannot create temporary file for $file"
    if ! awk -v path_line="$path_line" '
        {
            if (pending == "# Relay" && $0 == path_line) {
                pending = ""
                next
            }
            if (pending != "") {
                print pending
            }
            pending = $0
        }
        END { if (pending != "") print pending }
    ' "$file" > "$tmp"; then
        rm -f "$tmp"
        fail "cannot update $file"
    fi
    if cmp -s "$file" "$tmp"; then
        rm -f "$tmp"
    else
        mv -f "$tmp" "$file"
    fi
}

remove_skill() {
    dir=$1
    [ -e "$dir" ] || return 0
    [ -L "$dir" ] && {
        printf 'relay uninstall: leaving symlink %s unchanged\n' "$dir" >&2
        return 0
    }
    [ -f "$dir/SKILL.md" ] && [ -f "$dir/.relay-sha256" ] || return 0
    if command -v sha256sum >/dev/null 2>&1; then
        digest=$(sha256sum "$dir/SKILL.md" | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        digest=$(shasum -a 256 "$dir/SKILL.md" | awk '{print $1}')
    else
        printf 'relay uninstall: cannot verify managed Skill %s\n' "$dir" >&2
        return 0
    fi
    stamp=$(cat "$dir/.relay-sha256")
    if [ "$digest" != "$stamp" ]; then
        printf 'relay uninstall: kept modified Skill %s\n' "$dir" >&2
        return 0
    fi
    rm -f "$dir/SKILL.md" "$dir/.relay-sha256" "$dir/.relay-install.lock"
    if rmdir "$dir" 2>/dev/null; then
        return 0
    fi
    printf 'relay uninstall: kept %s because it contains other files\n' "$dir" >&2
}

home=${HOME:-}
[ -n "$home" ] || fail 'HOME is not set'
dest=${RELAY_INSTALL_DIR:-"$home/.local/bin"}
case "$dest" in
    /*) ;;
    *) fail 'RELAY_INSTALL_DIR must be an absolute path' ;;
esac

target=$dest/relay
if [ -L "$target" ]; then
    fail "refusing to remove symlink $target"
fi
if [ -e "$target" ]; then
    rm -f "$target" || fail "cannot remove $target; stop running relay first"
fi

shell=${SHELL:-}
case "$shell" in
    */zsh) profile=${ZDOTDIR:-"$home"}/.zshrc ;;
    */bash) profile="$home/.bashrc" ;;
    *) profile="$home/.profile" ;;
esac
quoted=$(printf '%s' "$dest" | sed "s/'/'\\''/g")
remove_path_line "$profile" "export PATH='$quoted':\"\$PATH\""
remove_block "$profile" '# RELAY:START completion' '# RELAY:END completion'

fish_file=${XDG_CONFIG_HOME:-"$home/.config"}/fish/completions/relay.fish
remove_block "$fish_file" '# RELAY:START completion' '# RELAY:END completion'
if [ -f "$fish_file" ] && [ ! -L "$fish_file" ] && [ ! -s "$fish_file" ]; then
    rm -f "$fish_file"
fi

remove_skill "${CLAUDE_CONFIG_DIR:-"$home/.claude"}/skills/relay-handoff"
remove_skill "${CODEX_HOME:-"$home/.codex"}/skills/relay-handoff"

printf 'Relay binary, managed shell completion, and managed Handoff Skill were removed.\n'
printf 'Relay data and encrypted credentials were kept. Remove RELAY_HOME manually only if you intend to delete them.\n'
