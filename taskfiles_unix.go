//go:build !windows

package main

import "syscall"

const taskOpenFlags = syscall.O_NONBLOCK
