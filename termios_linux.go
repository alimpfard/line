//go:build linux
// +build linux

package line

import (
	"golang.org/x/sys/unix"
)

func getTermios() (*unix.Termios, error) {
	return unix.IoctlGetTermios(unix.Stdin, unix.TCGETS)
}

func setTermios(t *unix.Termios) error {
	return unix.IoctlSetTermios(unix.Stdin, unix.TCSETS, t)
}
