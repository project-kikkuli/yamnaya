package proc

import "syscall"

// Signal sends sig to pid. Every signal ym sends a process it did not start goes through it, so tests can fence it in.
var Signal = syscall.Kill
