//go:build !linux && !darwin && !windows

package setup

import (
	"io"
	"os"
)

func isFileTerminal(_ *os.File) bool {
	return false
}

func enterRawMode(_ io.Reader, _ io.Writer) (func(), error) {
	return func() {}, nil
}

func readEscapeTail(_ io.Reader, _ []byte) (int, bool) {
	return 0, false
}
