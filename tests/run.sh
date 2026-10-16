#!/bin/sh
# Builds tasks, lays it out as rhun installs it, and runs tests/basic.txt headlessly in a project
# with a Makefile and a package.json. usage: tests/run.sh [RHUN]
set -e
cd "$(dirname "$0")/.."
RHUN=${1:-$(command -v rhun || echo "$HOME/.local/bin/rhun")}
case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) target=linux-x86_64 ;;
    Darwin-arm64) target=macos-arm64 ;;
    *) echo "no target for $(uname -s)-$(uname -m)"; exit 77 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/exts/rhun-editor/tasks
mkdir -p "$dir/bin" "$work/proj" "$work/config/rhun"
CGO_ENABLED=0 go build -o "$dir/bin/tasks-$target" .
cp extension.ini "$dir/"
printf 'all: build\n\nbuild:\n\techo building\n\ntest:\n\techo testing\n' > "$work/proj/Makefile"
printf '{"name":"x","scripts":{"lint":"eslint ."}}\n' > "$work/proj/package.json"
printf 'hello\n' > "$work/proj/a.txt"
printf '[files]\nrestore_session = false\nrestore_project = false\n[updates]\ncheck = false\n[git]\nenabled = false\n[ui]\nsidebar = false\nagents_panel = false\n[editor]\ncursor_blink = false\n' > "$work/config/rhun/config"
sed "s|proj/|$work/proj/|" tests/basic.txt > "$work/basic.txt"
HOME=$work XDG_CONFIG_HOME=$work/config XDG_STATE_HOME=$work/state \
    "$RHUN" "$work/proj" --headless 1000x600 --scale 1 --extensions "$work/exts" --script "$work/basic.txt" 2>&1 \
    | sed 's/pid=[1-9][0-9]*/pid=PID/' > "$work/out.txt"
if cmp -s "$work/out.txt" tests/basic.expected; then
    echo "ok   basic"
else
    echo "FAIL basic"; diff tests/basic.expected "$work/out.txt" || true
    echo "--- the extension's log:"; cat "$work"/state/rhun/extensions/*.log 2>/dev/null | tail -20; exit 1
fi
