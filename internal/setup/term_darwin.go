//go:build darwin

package setup

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

func isFileTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	_, err := ioctlGetTermios(int(f.Fd()))
	return err == nil
}

func enterRawMode(in io.Reader, _ io.Writer) (func(), error) {
	f, ok := in.(*os.File)
	if !ok || f == nil {
		return func() {}, nil
	}
	fd := int(f.Fd())
	orig, err := ioctlGetTermios(fd)
	if err != nil {
		return func() {}, nil
	}

	raw := *orig
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if err := ioctlSetTermios(fd, &raw); err != nil {
		return nil, err
	}

	saved := *orig
	return func() {
		_ = ioctlSetTermios(fd, &saved)
	}, nil
}

func readEscapeTail(in io.Reader, buf []byte) (int, bool) {
	f, ok := in.(*os.File)
	if !ok || f == nil {
		return 0, false
	}
	fd := int(f.Fd())
	cur, err := ioctlGetTermios(fd)
	if err != nil {
		return 0, false
	}

	timed := *cur
	timed.Cc[syscall.VMIN] = 0
	timed.Cc[syscall.VTIME] = 1 // 100ms timeout for trailing escape sequence bytes
	if err := ioctlSetTermios(fd, &timed); err != nil {
		return 0, true
	}
	defer func() {
		_ = ioctlSetTermios(fd, cur)
	}()

	for {
		n, rerr := syscall.Read(fd, buf)
		if rerr == syscall.EINTR {
			continue
		}
		if n < 0 {
			return 0, true
		}
		return n, true
	}
}

func ioctlGetTermios(fd int) (*syscall.Termios, error) {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCGETA), uintptr(unsafe.Pointer(&t)))
	if errno != 0 {
		return nil, errno
	}
	return &t, nil
}

func ioctlSetTermios(fd int, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCSETA), uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func termHeight(out io.Writer) int {
	f, ok := out.(*os.File)
	if !ok || f == nil {
		return 0
	}
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.Row)
}
