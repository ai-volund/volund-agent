//go:build !linux

package main

import "os/exec"

func applySandboxLimits(_ *exec.Cmd) {}
