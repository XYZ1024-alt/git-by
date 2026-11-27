//go:build windows

package setup

import (
	"io"
	"os"
	"syscall"
	"unsafe"
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
	kernel32DLL                    = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = kernel32DLL.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32DLL.NewProc("GetConsoleScreenBufferInfo")
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

type consoleCoord struct {
	X int16
	Y int16
}

type consoleSmallRect struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              consoleCoord
	CursorPosition    consoleCoord
	Attributes        uint16
	Window            consoleSmallRect
	MaximumWindowSize consoleCoord
}

func termSize(out io.Writer) (rows, cols int) {
	f, ok := out.(*os.File)
	if !ok || f == nil {
		return 0, 0
	}
	var info consoleScreenBufferInfo
	r1, _, _ := procGetConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return 0, 0
	}
	rows = int(info.Window.Bottom) - int(info.Window.Top) + 1
	if rows <= 0 {
		rows = 0
	}
	cols = int(info.Window.Right) - int(info.Window.Left) + 1
	if cols <= 0 {
		cols = 0
	}
	return rows, cols
}

func termHeight(out io.Writer) int {
	rows, _ := termSize(out)
	return rows
}

func termWidth(out io.Writer) int {
	_, cols := termSize(out)
	return cols
}
