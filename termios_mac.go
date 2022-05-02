//go:build darwin
// +build darwin

package line

import (
	"golang.org/x/sys/unix"
)

func getTermios() (*unix.Termios, error) {
	return unix.IoctlGetTermios(unix.Stdin, unix.TIOCGETA)
}

func setTermios(t *unix.Termios) error {
	return unix.IoctlSetTermios(unix.Stdin, unix.TIOCSETA, t)
}
