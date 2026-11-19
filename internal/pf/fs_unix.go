//go:build darwin || freebsd

package pf

import "syscall"

func statMtime(st *syscall.Stat_t) int64 { return st.Mtimespec.Sec }
