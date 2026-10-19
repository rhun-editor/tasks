#!/bin/sh
# Builds tasks, lays it out as rhun installs it, and runs each tests/NAME.txt headlessly in a project
# made for it, comparing what rhun prints with tests/NAME.expected:
#   basic   a Makefile and a package.json: the published tasks, the approval, a run, Run Task
#   go      go build ./... with three errors: problems and their marks (go on PATH)
#   cargo   cargo build with E0425: a two-line rustc problem (cargo on PATH)
#   server  a dev server task and one depending on it: it runs once the server is ready, which keeps
#           running; the debugger's prelaunch hook runs it by name (python3 on PATH)
# usage: tests/run.sh [RHUN [NAME...]]   (a go.work with the SDK when it is unreleased: GOWORK=…;
# TASKS_TEST_OUT=DIR keeps what rhun printed)
set -e
cd "$(dirname "$0")/.."
RHUN=${1:-$(command -v rhun || echo "$HOME/.local/bin/rhun")}
[ $# -gt 0 ] && shift
names=${*:-basic go cargo server}
case "$(uname -s)-$(uname -m)" in
    Linux-x86_64) target=linux-x86_64 ;;
    Darwin-arm64) target=macos-arm64 ;;
    *) echo "no target for $(uname -s)-$(uname -m)"; exit 77 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/exts/rhun-editor/tasks
mkdir -p "$dir/bin" "$work/config/rhun"
CGO_ENABLED=0 go build -o "$dir/bin/tasks-$target" .
cp extension.ini "$dir/"
printf '[files]\nrestore_session = false\nrestore_project = false\n[updates]\ncheck = false\n[git]\nenabled = false\n[ui]\nsidebar = false\nagents_panel = false\n[editor]\ncursor_blink = false\n' > "$work/config/rhun/config"
# the toolchains keep their homes; everything of rhun's goes to $work
export RUSTUP_HOME="${RUSTUP_HOME:-$HOME/.rustup}" CARGO_HOME="${CARGO_HOME:-$HOME/.cargo}"
export GOPATH="$work/gopath" GOCACHE="$work/gocache" GOTOOLCHAIN=local
port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')

setup() {
    p=$work/$1
    mkdir -p "$p"
    case $1 in
    basic)
        printf 'all: build\n\nbuild:\n\techo building\n\ntest:\n\techo testing\n' > "$p/Makefile"
        printf '{"name":"x","scripts":{"lint":"echo linted"}}\n' > "$p/package.json"
        printf 'hello\n' > "$p/a.txt" ;;
    go)
        printf 'module example.com/w1\n\ngo 1.21\n' > "$p/go.mod"
        printf 'package main\n\nfunc main() {\n\tx := undefinedThing\n}\n' > "$p/main.go"
        printf 'package main\n\nvar s int = "s"\n' > "$p/other.go" ;;
    cargo)
        printf '[package]\nname = "w2"\nversion = "0.1.0"\nedition = "2021"\n' > "$p/Cargo.toml"
        mkdir -p "$p/src"
        printf 'fn main() {\n    let y = 1;\n    println!("{}", x + y);\n}\n' > "$p/src/main.rs" ;;
    server)
        mkdir -p "$p/.rhun"
        cat > "$p/.rhun/tasks.ini" <<INI
[serve]
label = Dev server
argv = python3 -m http.server $port --bind 127.0.0.1
ready = Serving HTTP on {_}

[smoke]
label = Smoke
argv = python3 -c "import urllib.request as u; print('status', u.urlopen('http://127.0.0.1:$port/').status)"
depends = serve
INI
        printf 'hello\n' > "$p/index.html" ;;
    esac
}

fails=0
for name in $names; do
    setup "$name"
    sed "s|PROJ/|$work/$name/|g" "tests/$name.txt" > "$work/$name.txt"
    GOWORK=off HOME=$work XDG_CONFIG_HOME=$work/config XDG_STATE_HOME=$work/state TERM=xterm-256color \
        "$RHUN" "$work/$name" --headless 1000x600 --scale 1 --extensions "$work/exts" --script "$work/$name.txt" 2>&1 \
        | sed -e 's/pid=[1-9][0-9]*/pid=PID/' -e "s|$work|WORK|g" -e 's/after [0-9.]* s/after T s/' \
              -e "s/$port/PORT/g" > "$work/$name.out"
    [ -z "$TASKS_TEST_OUT" ] || cp "$work/$name.out" "$TASKS_TEST_OUT/"     # to make a new .expected
    if cmp -s "$work/$name.out" "tests/$name.expected"; then
        echo "ok   $name"
    else
        echo "FAIL $name"; diff "tests/$name.expected" "$work/$name.out" || true
        fails=1
    fi
done
if [ $fails = 1 ]; then
    echo "--- the extension's log:"; cat "$work"/state/rhun/extensions/*.log 2>/dev/null | tail -20; exit 1
fi
