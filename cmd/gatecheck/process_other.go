//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package main

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
