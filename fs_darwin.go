package tail

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"unsafe"

	"golang.org/x/sys/unix"
)

// from https://github.com/fluent/fluent-bit/blob/2b80bb64c3feb9979126c13f4409ce10afd8b23e/plugins/in_tail/tail_file.c#L914-L963
func getFileName(f *os.File) (string, error) {
	buf := make([]byte, 2048)
	data := (*reflect.StringHeader)(unsafe.Pointer(&buf)).Data
	fd := f.Fd()
	_, err := unix.FcntlInt(fd, unix.F_GETPATH, int(data))
	if err != nil {
		return "", err
	}
	idx := bytes.IndexByte(buf, 0)
	if idx < 0 {
		return "", errors.New("tail: fail to get path")
	}
	return string(buf[:idx]), nil
}
