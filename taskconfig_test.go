package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskConfigStrict(t *testing.T) {
	valid := `{"version":1,"tasks":[{"id":"build","label":"Build 😀","command":"printf 'hello'"}]}`
	got, err := decodeTaskConfig([]byte(valid))
	if err != nil || len(got) != 1 || got[0].name != "config_build" || got[0].title != "Build 😀" {
		t.Fatal(got, err)
	}
	for _, value := range []string{
		`null`, `[]`, `{}`, `{"version":2,"tasks":[]}`, `{"version":1.0,"tasks":[]}`, `{"version":1,"tasks":null}`, `{"version":1,"tasks":[],"other":1}`, `{"version":1,"version":1,"tasks":[]}`, valid + `{}`,
		`{"version":1,"tasks":[{"id":"x","id":"y","label":"x","command":"true"}]}`,
		`{"version":1,"tasks":[{"id":"x","label":"x","command":"true","cwd":".."}]}`,
		`{"version":1,"tasks":[{"id":"x","label":"x","command":null}]}`,
		`{"version":1,"tasks":[{"id":"UPPER","label":"x","command":"true"}]}`,
		`{"version":1,"tasks":[{"id":"x","label":"x","command":"a\ntrue"}]}`,
		`{"version":1,"tasks":[{"id":"x","label":"x","command":"a\u0000true"}]}`,
		`{"version":1,"tasks":[{"id":"x","label":"x","command":"a\ud800true"}]}`,
		`{"version":1,"tasks":[{"id":"x","label":"x","command":"\udc00"}]}`,
		strings.Replace(valid, `"build"`, `"`+strings.Repeat("x", 34)+`"`, 1),
		strings.Replace(valid, "Build 😀", strings.Repeat("x", 129), 1),
		strings.Replace(valid, "Build 😀", "\xff", 1),
	} {
		if _, err := decodeTaskConfig([]byte(value)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	duplicate := `{"version":1,"tasks":[{"id":"x","label":"A","command":"true"},{"id":"x","label":"B","command":"false"}]}`
	if _, err := decodeTaskConfig([]byte(duplicate)); err == nil {
		t.Fatal("duplicate task id")
	}
	if _, err := decodeTaskConfig([]byte(strings.Repeat(" ", maxTaskConfigBytes) + valid)); err == nil {
		t.Fatal("oversized config")
	}
	pair := strings.Replace(valid, "Build 😀", `Build \ud83d\ude00`, 1)
	got, err = decodeTaskConfig([]byte(pair))
	if err != nil || got[0].title != "Build 😀" {
		t.Fatal(got, err)
	}
}
func TestTaskConfigCountAndIDs(t *testing.T) {
	var entries []string
	for i := 0; i < 129; i++ {
		entries = append(entries, fmt.Sprintf(`{"id":"x%d","label":"Task %d","command":"true"}`, i, i))
	}
	for _, n := range []int{128, 129} {
		value := `{"version":1,"tasks":[` + strings.Join(entries[:n], ",") + `]}`
		got, err := decodeTaskConfig([]byte(value))
		if n == 128 && (err != nil || len(got) != 128) || n == 129 && err == nil {
			t.Fatal(n, len(got), err)
		}
	}
	value := `{"version":1,"tasks":[{"id":"` + strings.Repeat("a", 33) + `","label":"x","command":"true"}]}`
	got, err := decodeTaskConfig([]byte(value))
	if err != nil || len(got[0].name) != 40 {
		t.Fatal(got, err)
	}
}
func TestReadTaskConfigBoundedRoot(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".rhun"), 0o755)
	if got, err := readConfiguredTasks(dir); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	path := filepath.Join(dir, taskConfigPath)
	os.WriteFile(path, []byte(`{"version":1,"tasks":[]}`), 0o644)
	if _, err := readConfiguredTasks(dir); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(strings.Repeat("x", maxTaskConfigBytes+1)), 0o644)
	if _, err := readConfiguredTasks(dir); err == nil {
		t.Fatal("oversized file")
	}
	os.Remove(path)
	os.Mkdir(path, 0o755)
	if _, err := readConfiguredTasks(dir); err == nil {
		t.Fatal("directory")
	}
	os.Remove(path)
	outside := filepath.Join(t.TempDir(), "outside.json")
	os.WriteFile(outside, []byte(`{"version":1,"tasks":[]}`), 0o644)
	if err := os.Symlink(outside, path); err != nil {
		t.Skip(err)
	}
	if _, err := readConfiguredTasks(dir); err == nil {
		t.Fatal("symlink escaped workspace")
	}
}
func TestDiscoveredTaskLimitsAndCollisionKeys(t *testing.T) {
	dir := t.TempDir()
	var makefile strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&makefile, "%s%d:\n", strings.Repeat("a", 40), i)
	}
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(makefile.String()), 0o644)
	result := discoverTasks(dir)
	if len(result.tasks) != 12 || result.warning != "" {
		t.Fatal(result)
	}
	names := map[string]bool{}
	for _, task := range result.tasks {
		if len(task.name) > 40 || names[task.name] {
			t.Fatal(task.name)
		}
		names[task.name] = true
	}
	makefile.Reset()
	for i := 0; i < 1001; i++ {
		fmt.Fprintf(&makefile, "target%d:\n", i)
	}
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(makefile.String()), 0o644)
	result = discoverTasks(dir)
	if len(result.tasks) != 872 || result.warning == "" {
		t.Fatal(len(result.tasks), result.warning)
	}
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(strings.Repeat("x", maxTaskSourceBytes+1)), 0o644)
	result = discoverTasks(dir)
	if len(result.tasks) != 0 || result.warning == "" {
		t.Fatal(len(result.tasks), result.warning)
	}
}
func TestNPMTaskNamesAreShellArguments(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"a;echo unwanted":"true","normal":"true","-option":"true"}}`), 0o644)
	got := discoverTasks(dir)
	if len(got.tasks) != 3 || got.warning != "" {
		t.Fatal(got)
	}
	if got.tasks[0].command != "npm run -- -option" || got.tasks[1].command != "npm run 'a;echo unwanted'" || got.tasks[2].command != "npm run normal" {
		t.Fatal(got.tasks)
	}
}

func TestDiscoveryUsesMakeDefaultFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte("standard:\n\ttrue\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "GNUmakefile"), []byte("preferred:\n\ttrue\n"), 0o644)
	result := discoverTasks(dir)
	if len(result.tasks) != 1 || result.tasks[0].name != "make_preferred" || result.tasks[0].command != "make preferred" {
		t.Fatal(result)
	}
}

func TestOversizedPreferredMakefileDoesNotAdvertiseFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "GNUmakefile"), []byte(strings.Repeat("#", maxTaskSourceBytes+1)), 0o644)
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte("fallback:\n\ttrue\n"), 0o644)
	result := discoverTasks(dir)
	if len(result.tasks) != 0 || result.warning == "" {
		t.Fatal("advertised targets from a file make will not select", result)
	}
}

func TestPowerShellScriptNamesAreNotSplatted(t *testing.T) {
	for _, word := range []string{"@args", "@scope/task"} {
		if got := taskShellWordFor(word, true); got != "'"+word+"'" {
			t.Fatal(word, got)
		}
		if got := taskShellWordFor(word, false); got != word {
			t.Fatal(word, got)
		}
	}
}
