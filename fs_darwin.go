package tail

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"unsafe"

	"golang.org/x/sys/unix"
)

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
