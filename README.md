# tasks

The project's tasks in [rhun](https://rhun.app)'s palette. When a project opens, the extension
reads its task files. Run Task lists up to 1,000 tasks, and the first 64 have individual palette commands:

| File | Tasks |
| --- | --- |
| `.rhun/tasks.json` | explicit named shell tasks |
| `Makefile` | every target that is not a pattern or a variable: `make build`, `make test`, … |
| `justfile` | every recipe not starting with `_`: `just deploy` |
| `package.json` | every script, run with npm, pnpm, yarn or bun by the lockfile present |
| `Cargo.toml` | `cargo build`, `test`, `run`, `check`, `clippy` |
| `go.mod` | `go build ./...`, `test ./...`, `vet ./...`, `run .` |

On builds with managed tasks, rhun tracks each reviewed run and retains its output. **Stop Task**,
**Restart Task** and **Show Task Output** manage retained runs. Earlier builds keep the terminal flow. In the managed output pane,
Home/End and Page Up/Down move through retained output. Ctrl+C copies the retained text,
Ctrl+Shift+S stops the selected run, and Ctrl+Shift+R reviews a restart. On macOS, use Command
for these Ctrl shortcuts. Escape hides the pane while output continues to drain. End resumes
following new output after scrolling through history.

Running a task shows its command line in rhun's prompt; Enter runs it, Esc dismisses it. **Run Task…** (ctrl+alt+r) lists all of them; **Rescan Tasks** reads the
files again. They are also checked every two seconds, so a new target appears on its own.
On hosts that agree to protocol 1.1's `commands` feature, removed tasks leave the palette too.
On earlier hosts, they leave when the extension restarts.

## Project tasks

Create `.rhun/tasks.json` to list explicit tasks before discovered ones:

```json
{
  "version": 1,
  "tasks": [
    { "id": "build", "label": "Build", "command": "go build ./..." }
  ]
}
```

The command runs in the project folder after review. IDs use 1 to 33 lowercase letters,
digits or underscores. The file accepts up to 128 tasks and 64 KiB. Invalid edits keep the
last valid task definitions for that project and show an error. Unknown fields, duplicate
keys/IDs and multiline commands are rejected. Nothing runs during discovery.

Discovered sources are capped at 1 MiB each and 872 tasks in total. Oversized sources or
lists show a warning; the retained tasks remain available through Run Task.

## Needs

Project: read (the task files). Terminal: yes (the proposed command lines; nothing runs without
the user pressing Enter).

## Build and test

`go test -race ./...` covers parsing, bounded reads, task selection and managed run history.
`python3 tests/lifecycle.py` and `python3 tests/managed-lifecycle.py` drive the extension
against protocol peers, including old hosts, label refresh, invalid config and project changes. `tests/run.sh [RHUN]` builds the extension, lays
it out as rhun installs it in a project with a Makefile and a package.json, and runs
`tests/basic.txt` headlessly: the registered commands, the proposed command line, the pick
list, and a task run in the terminal. Releases are built by the reusable workflow from
`rhun-editor/extension-template`.
