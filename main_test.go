package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScan(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Makefile"), []byte(".PHONY: all\nall: build\n\nbuild:\n\tgo build\n\ntest: build\n\tgo test\n\n%.o: %.c\n\tcc\n\nVAR := 1\ninstall-local:\n\tcp a b\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"lint":"eslint .","build":"tsc"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "justfile"), []byte("# comment\ndefault:\n  just --list\n\n_hidden:\n  true\n\ndeploy env='prod':\n  echo {{env}}\n\nset shell := [\"bash\"]\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	got := scan(dir)
	var titles []string
	for _, g := range got {
		titles = append(titles, g.name+"="+g.command)
	}
	want := []string{"make_all=make all", "make_build=make build", "make_test=make test", "make_install_local=make install-local",
		"just_default=just default", "just_deploy=just deploy", "npm_build=npm run build", "npm_lint=npm run lint",
		"go_build=go build ./...", "go_test=go test ./...", "go_vet=go vet ./...", "go_run=go run ."}
	if len(titles) != len(want) {
		t.Fatalf("got %v", titles)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("%d: got %q want %q", i, titles[i], want[i])
		}
	}
}
