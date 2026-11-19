//go:build !darwin && !freebsd

package pf

func sysctlRaw(name string) ([]byte, bool) { return nil, false }
