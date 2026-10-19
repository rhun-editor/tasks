package main

import (
	"fmt"
	"io"
	"os"
)

// readTaskSource opens through a workspace root so relative symlinks cannot escape it.
// The handle is classified before any content read; POSIX opens are nonblocking for FIFOs.
func readTaskSource(project, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(project)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_RDONLY|taskOpenFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
	}
	return data, nil
}
func taskSourceExists(project, name string) bool {
	root, err := os.OpenRoot(project)
	if err != nil {
		return false
	}
	defer root.Close()
	st, err := root.Stat(name)
	return err == nil && st.Mode().IsRegular()
}
