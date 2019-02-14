// +build dragonfly freebsd linux netbsd openbsd

package tail

import (
	"fmt"
	"os"
)

// from https://github.com/fluent/fluent-bit/blob/2b80bb64c3feb9979126c13f4409ce10afd8b23e/plugins/in_tail/tail_file.c#L914-L963
func getFileName(f *os.File) (string, error) {
	fd := f.Fd()
	return os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd))
}
