package tail

import (
	"bytes"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// from https://github.com/fluent/fluent-bit/blob/2b80bb64c3feb9979126c13f4409ce10afd8b23e/plugins/in_tail/tail_file.c#L914-L963
func getFileName(f *os.File) (string, error) {
	var buf [2048]byte
	data := uintptr(unsafe.Pointer(&buf[0]))
	fd := f.Fd()

	var err error
	for {
		_, err = unix.FcntlInt(fd, unix.F_GETPATH, int(data))
		if err != unix.EINTR {
			// According to https://golang.org/doc/go1.14#runtime
			// A consequence of the implementation of preemption is that on Unix systems, including Linux and macOS
			// systems, programs built with Go 1.14 will receive more signals than programs built with earlier releases.
			//
			// This causes unix.FcntlInt sometimes fails with EINTR errors.
			// We need to retry in this case.
			break
		}
	}
	if err != nil {
		return "", err
	}

	idx := bytes.IndexByte(buf[:], 0)
	if idx < 0 {
		return "", errors.New("tail: fail to get path")
	}
	return string(buf[:idx]), nil
}
