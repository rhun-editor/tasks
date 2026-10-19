package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

func describe(e entry) string {
	t := e.task
	cmd := t.Program.Shell
	if cmd == "" {
		cmd = strings.Join(t.Program.Argv, "|")
	}
	s := e.name + " " + t.ID + " " + cmd
	if t.Group != "" {
		s += " group=" + t.Group
	}
	if t.Default {
		s += " default"
	}
	for _, m := range t.Matchers {
		s += " matcher=" + m.Builtin
	}
	if len(t.Ready) > 0 {
		s += " background"
	}
	return s
}

func TestScan(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(".PHONY: all\nall: build\n\nbuild:\n\tgo build\n\ntest: build\n\tgo test\n\n%.o: %.c\n\tcc\n\nVAR := 1\ninstall-local:\n\tcp a b\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"lint":"eslint .","build":"tsc -p .","dev":"vite"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "justfile"), []byte("# comment\ndefault:\n  just --list\n\n_hidden:\n  true\n\ndeploy env='prod':\n  echo {{env}}\n\nset shell := [\"bash\"]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	var got []string
	for _, e := range discoverTasks(dir, false).tasks {
		got = append(got, describe(e))
	}
	want := []string{
		"make_all make.all make all group=build default matcher=gcc",
		"make_build make.build make build group=build matcher=gcc",
		"make_test make.test make test group=test default matcher=gcc",
		"make_install_local make.install-local make install-local matcher=gcc",
		"just_default just.default just default",
		"just_deploy just.deploy just deploy",
		"npm_build npm.build npm run build group=build matcher=tsc",
		"npm_dev npm.dev npm run dev background",
		"npm_lint npm.lint npm run lint",
		"go_build go.build go build ./... group=build matcher=go",
		"go_test go.test go test ./... group=test matcher=go",
		"go_vet go.vet go vet ./... matcher=go",
		"go_run go.run go run .",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
	// what rhun gets is a valid list
	var list []rhunext.Task
	for _, e := range scan(dir) {
		list = append(list, e.task)
	}
	if _, err := rhunext.EncodeTaskList(list); err != nil {
		t.Fatal(err)
	}
}

func TestScanOnWindowsRunsArgv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"tsc"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0o644)
	got := discoverTasks(dir, true).tasks
	if got[0].task.Program.Shell != "" || !reflect.DeepEqual(got[0].task.Program.Argv, []string{"npm.cmd", "run", "build"}) {
		t.Fatalf("%+v", got[0].task.Program)
	}
	if !reflect.DeepEqual(got[1].task.Program.Argv, []string{"cargo", "build"}) || got[1].task.Matchers[0].Builtin != "rustc" {
		t.Fatalf("%+v", got[1].task)
	}
}

func TestTaskINI(t *testing.T) {
	text := `# the project's tasks
[serve]
label = Dev server
argv = python3 -m http.server 8765
cwd = web
env.PORT = 8765
ready = Serving HTTP on {_}
depends = build lint
depends.order = parallel

[build]
command = go build ./...
group = build
default = yes
matcher = go

[lint]
command = ./lint.sh
matcher.severity = warning
matcher.1 = {file}:{line}: {message}
`
	got, err := decodeTaskINI(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].name != "config_serve" || got[0].task.Label != "Dev server" ||
		!reflect.DeepEqual(got[0].task.Program.Argv, []string{"python3", "-m", "http.server", "8765"}) ||
		got[0].task.Program.Cwd != "web" || got[0].task.Program.Env[0] != (rhunext.EnvVar{Name: "PORT", Value: "8765"}) ||
		!got[0].task.Parallel || got[0].task.Ready[0] != "Serving HTTP on {_}" {
		t.Fatalf("%+v", got[0].task)
	}
	if got[1].task.Label != "build" || !got[1].task.Default || got[1].task.Matchers[0].Builtin != "go" {
		t.Fatalf("%+v", got[1].task)
	}
	if got[2].task.Matchers[0].Text != "severity warning\n1 {file}:{line}: {message}\n" {
		t.Fatalf("%q", got[2].task.Matchers[0].Text)
	}
	for text, want := range map[string]string{
		"[Bad]\ncommand = x\n":                              "a task id is",
		"[a]\ncommand = x\ncolour = red\n":                  `unknown key "colour"`,
		"[a]\ncommand = x\nargv = y\n":                      "command or argv, not both",
		"[a]\ncommand = x\nready = {nope}\n":                "unknown field",
		"[a]\ncommand = x\n[a]\ncommand = y\n":              "[a] twice",
		"command = x\n":                                     "KEY = VALUE in a [task] section",
		"[a]\n":                                             "command: 1 to 4096 bytes",
		"[a]\ncommand = x\nmatcher.2 = {file}: {message}\n": "steps go 1, 2, 3, 4 in order",
	} {
		if _, err := decodeTaskINI(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %s", text, err, want)
		}
	}
}

func TestSplitArgv(t *testing.T) {
	got, err := splitArgv(`python3  -c "print(""a b"")" ""`)
	if err != nil || !reflect.DeepEqual(got, []string{"python3", "-c", `print("a b")`, ""}) {
		t.Fatal(got, err)
	}
	if _, err := splitArgv(`a "b`); err == nil {
		t.Fatal("an open quote accepted")
	}
}
