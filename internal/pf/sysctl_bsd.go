//go:build darwin || freebsd

package pf

import "golang.org/x/sys/unix"

func sysctlRaw(name string) ([]byte, bool) {
	b, err := unix.SysctlRaw(name)
	if err != nil {
		return nil, false
	}
	return b, true
}
