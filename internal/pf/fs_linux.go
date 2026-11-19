//go:build linux

package pf

import "syscall"

func statMtime(st *syscall.Stat_t) int64 { return st.Mtim.Sec }
