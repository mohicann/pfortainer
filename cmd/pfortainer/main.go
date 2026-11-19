// pfortainer: FreeBSD + Podman 관리 웹 UI (Go). `pfortainer -hostd`는 root 권한 host agent.
package main

import "github.com/mohicann/pfortainer/internal/pf"

func main() { pf.Main() }
