// +build dragonfly freebsd linux netbsd openbsd

package tail

import (
	"fmt"
	"os"
)

func getFileName(f *os.File) (string, error) {
	fd := f.Fd()
	return os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd))
}
