# tasks

The project's tasks for [rhun](https://rhun.app)'s **Run Task**. When a project opens, the extension
reads its task files and publishes the tasks to rhun, which lists them, runs them in terminal tabs
and turns their errors into problems:

| File | Tasks |
| --- | --- |
| `.rhun/tasks.ini` | your own, listed first (below) |
| `Makefile` | every target that is not a pattern or a variable: `make build`, `make test`, … (gcc and clang errors) |
| `justfile` | every recipe not starting with `_`: `just deploy` |
| `package.json` | every script, run with npm, pnpm, yarn or bun by the lockfile present; a script running `tsc` gets its errors; `dev`, `start` and `serve` are dev servers, ready once they print a local URL |
| `Cargo.toml` | `cargo build`, `test`, `run`, `check`, `clippy` (rustc errors) |
| `go.mod` | `go build ./...`, `test ./...`, `vet ./...`, `run .` (go errors) |

**Run Build Task** (ctrl+shift+B) runs the build group's default (`go build ./...`, `cargo build`,
`make all`, `npm run build`), **Run Test Task** the test group's. **Run Task…** (ctrl+alt+r) lists all
of them; **Stop Task**, **Restart Task** and **Show Task** are rhun's. The first 64 tasks are also
commands of their own. The files are checked every two seconds, so a new target appears on its
own; **Rescan Tasks** reads them at once.

The first time a task runs in a project, rhun asks, showing the command: **Allow** remembers that
for the project. Tasks run as you, with your login shell's environment, each in its own terminal
tab, reused when it runs again. See rhun's guide for the panel, problems and dependencies.

## Project tasks

Create `.rhun/tasks.ini`, one section per task (its id: lowercase letters, digits, `_ . -`):

```ini
[serve]
label = Dev server
argv = python3 -m http.server 8765
cwd = web
env.PORT = 8765
ready = Serving HTTP on {_}

[smoke]
label = Smoke test
command = curl -fsS http://127.0.0.1:8765/
depends = serve

[lint]
command = ./lint.sh
group = build
matcher.severity = warning
matcher.1 = {file}:{line}: {message}
```

| Key | Meaning |
| --- | --- |
| `label` | what Run Task and the tab show (default: the id) |
| `command` | a shell line (`/bin/sh -c`; PowerShell on Windows) |
| `argv` | or the program and its arguments, split on blanks; `"quote words"` that hold blanks |
| `cwd` | the folder, relative to the project |
| `env.NAME`, `unset` | environment variables to set; names to remove |
| `group`, `default` | `build` or `test`; `default = yes` makes it its group's default |
| `depends`, `depends.order` | task ids (yours or discovered ones, like `go.build`) to run first, in `sequence` (the default) or `parallel` |
| `ready`, `busy` | patterns: a background task is done for what depends on it when a line matches `ready`; `busy` starts a new cycle |
| `matcher` | built-in matchers: `go`, `gcc`, `rustc`, `tsc`, `msbuild` |
| `matcher.1` … `matcher.4`, `matcher.severity`, `matcher.columns`, `matcher.loop` | your own matcher's steps (rhun's pattern language: `{file}`, `{line}`, `{col}`, `{severity}`, `{code}`, `{message}`, `{_}`) |
| `detail`, `reveal` | a line beside the label; `show`, `focus` or `silent` when it starts |

The file accepts up to 128 tasks and 64 KiB; an invalid edit keeps the last valid tasks and shows
the error. Nothing runs during discovery. Discovered sources are capped at 1 MiB each and 872 tasks
in total.

## Needs

Project: read (the task files). Terminal: run (rhun runs the tasks after you allow it in a project).
Tasks 0.2 needs rhun 0.18 or later; on an older rhun, Run Task says so.

## Build and test

`go test ./...` covers discovery and `.rhun/tasks.ini`. `python3 tests/lifecycle.py` drives the
extension against protocol peers: old hosts, command removal, the published list and its changes.
`tests/check.sh` runs the conformance checker. `tests/run.sh [RHUN]` builds the extension, lays it
out as rhun installs it and runs four headless workflows with the real tools: a Makefile project
(the approval, a run, Run Task), `go build ./...` with three errors and `cargo build` with one
(problems and marks), and a dev server another task waits for. Releases are built by the
reusable workflow from `rhun-editor/extension-template`.
