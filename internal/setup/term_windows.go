//go:build windows

package setup

import (
	"io"
	"os"
	"syscall"
)

const (
	enableProcessedInput        uint32 = 0x0001
	enableLineInput             uint32 = 0x0002
	enableEchoInput             uint32 = 0x0004
	enableVirtualTerminalInput  uint32 = 0x0200
	enableProcessedOutput       uint32 = 0x0001
	enableVirtualTerminalOutput uint32 = 0x0004
)

var (
	kernel32DLL        = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode = kernel32DLL.NewProc("SetConsoleMode")
)

func isFileTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

func setConsoleMode(handle syscall.Handle, mode uint32) error {
	r1, _, err := procSetConsoleMode.Call(uintptr(handle), uintptr(mode))
	if r1 == 0 {
		if err != nil && err != syscall.Errno(0) {
			return err
		}
		return syscall.EINVAL
	}
	return nil
}

func enterRawMode(in io.Reader, out io.Writer) (func(), error) {
	var (
		inHandle    syscall.Handle
		origInMode  uint32
		hasInMode   bool
		outHandle   syscall.Handle
		origOutMode uint32
		hasOutMode  bool
	)

	if inFile, ok := in.(*os.File); ok && inFile != nil {
		h := syscall.Handle(inFile.Fd())
		var mode uint32
		if err := syscall.GetConsoleMode(h, &mode); err == nil {
			inHandle = h
			origInMode = mode
			hasInMode = true

			baseRaw := mode &^ (enableProcessedInput | enableLineInput | enableEchoInput)
			if err := setConsoleMode(h, baseRaw|enableVirtualTerminalInput); err != nil {
				if err2 := setConsoleMode(h, baseRaw); err2 != nil {
					return nil, err2
				}
			}
		}
	}

	if outFile, ok := out.(*os.File); ok && outFile != nil {
		h := syscall.Handle(outFile.Fd())
		var mode uint32
		if err := syscall.GetConsoleMode(h, &mode); err == nil {
			outHandle = h
			origOutMode = mode
			hasOutMode = true
			_ = setConsoleMode(h, mode|enableProcessedOutput|enableVirtualTerminalOutput)
		}
	}

	return func() {
		if hasOutMode {
			_ = setConsoleMode(outHandle, origOutMode)
		}
		if hasInMode {
			_ = setConsoleMode(inHandle, origInMode)
		}
	}, nil
}

func readEscapeTail(in io.Reader, _ []byte) (int, bool) {
	if f, ok := in.(*os.File); ok && isFileTerminal(f) {
		return 0, true
	}
	return 0, false
}
