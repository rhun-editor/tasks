//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTaskConfigFIFOHasNoBlockingRead(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".rhun"), 0o755)
	if err := syscall.Mkfifo(filepath.Join(dir, taskConfigPath), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readConfiguredTasks(dir); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO open blocked")
	}
}
