package service

import (
	"os"
	"syscall"
)

func syscallKillSelf() error { return syscall.Kill(os.Getpid(), syscall.SIGKILL) }
