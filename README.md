# tasks

The project's tasks in [rhun](https://rhun.app)'s palette. When a project opens, the extension
reads its task files and registers a command for each task:

| File | Tasks |
| --- | --- |
| `Makefile` | every target that is not a pattern or a variable: `make build`, `make test`, … |
| `justfile` | every recipe not starting with `_`: `just deploy` |
| `package.json` | every script, run with npm, pnpm, yarn or bun by the lockfile present |
| `Cargo.toml` | `cargo build`, `test`, `run`, `check`, `clippy` |
| `go.mod` | `go build ./...`, `test ./...`, `vet ./...`, `run .` |

Running a task shows its command line in rhun's prompt; Enter runs it in the terminal panel,
Esc dismisses it. **Run Task…** (ctrl+alt+r) lists all of them; **Rescan Tasks** reads the
files again. They are also checked every two seconds, so a new target appears on its own (a
task that disappears stays in the palette until the extension restarts: the protocol has no
unregister).

## Needs

Project: read (the task files). Terminal: yes (the proposed command lines; nothing runs without
the user pressing Enter).

## Build and test

`go test ./...` covers the task file parsing. `tests/run.sh [RHUN]` builds the extension, lays
it out as rhun installs it in a project with a Makefile and a package.json, and runs
`tests/basic.txt` headlessly: the registered commands, the proposed command line, the pick
list, and a task run in the terminal. Releases are built by the reusable workflow from
`rhun-editor/extension-template`.
