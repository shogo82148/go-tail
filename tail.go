package tail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	fsnotify "github.com/fsnotify/fsnotify"
)

const (
	openRetryInterval = time.Second
	tailOldFileDelay  = 15 * time.Second
	linesCapacity     = 1024
	errorsCapacity    = 16
)

// Line is a line of the target file.
type Line struct {
	Text string
	Time time.Time
}

// Tail tails a file.
type Tail struct {
	Lines  <-chan *Line
	Errors <-chan error

	opts     Options
	lines    chan<- *Line
	errors   chan<- error
	filename string
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
}

// Options is options for Tail
type Options struct {
	// MaxBytesLine is maximum length of lines in bytes.
	// If it is zero, there is no limit.
	MaxBytesLine int64
}

type tail struct {
	parent *Tail

	file    *os.File
	reader  *bufio.Reader
	watcher *fsnotify.Watcher
	buf     bytes.Buffer
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewTailFile starts tailing a file with opt options.
func NewTailFileWithOptions(filename string, opts Options) (*Tail, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan *Line, linesCapacity)
	errs := make(chan error, errorsCapacity)
	parent := &Tail{
		Lines:    lines,
		Errors:   errs,
		opts:     opts,
		lines:    lines,
		errors:   errs,
		filename: filename,
		ctx:      ctx,
		cancel:   cancel,
	}

	parent.wg.Add(1)
	go func() {
		defer parent.wg.Done()
		parent.runFile(os.SEEK_END)
	}()
	go parent.wait()

	return parent, nil
}

// NewTailFile starts tailing a file with the default configuration.
func NewTailFile(filename string) (*Tail, error) {
	return NewTailFileWithOptions(filename, Options{})
}

// NewTailReader starts tailing io.Reader
func NewTailReaderWithOptions(reader io.Reader, opts Options) (*Tail, error) {
	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan *Line, linesCapacity)
	errs := make(chan error, errorsCapacity)
	parent := &Tail{
		Lines:  lines,
		Errors: errs,
		opts:   opts,
		lines:  lines,
		errors: errs,
		ctx:    ctx,
		cancel: cancel,
	}
	r := ctxReader{
		ctx: ctx,
		r:   reader,
	}
	t := &tail{
		parent: parent,
		reader: parent.newReader(r),
		ctx:    ctx,
		cancel: cancel,
	}

	parent.wg.Add(1)
	go func() {
		defer parent.wg.Done()
		t.runReader()
	}()
	go parent.wait()

	return parent, nil
}

// NewTailReader starts tailing io.Reader with the default configuration.
func NewTailReader(reader io.Reader) (*Tail, error) {
	return NewTailReaderWithOptions(reader, Options{})
}

// Close stops tailing the file.
func (t *Tail) Close() error {
	t.cancel()
	t.wg.Wait()
	return nil
}

func (t *Tail) wait() {
	t.wg.Wait()
	close(t.errors)
	close(t.lines)
}

// open opens the target file.
// If it does not exist, wait for creating new file.
func (t *Tail) open(seek int) (*tail, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	for {
		file, err := os.Open(t.filename)
		if err == nil {
			// success, seek and watch the file.
			if _, err := file.Seek(0, seek); err != nil {
				file.Close()
				watcher.Close()
				return nil, err
			}
			if err := watcher.Add(t.filename); err != nil {
				file.Close()
				watcher.Close()
				return nil, err
			}
			ctx, cancel := context.WithCancel(t.ctx)
			r := ctxReader{
				ctx: ctx,
				r:   file,
			}

			return &tail{
				parent:  t,
				file:    file,
				reader:  t.newReader(r),
				watcher: watcher,
				ctx:     ctx,
				cancel:  cancel,
			}, nil
		}

		// fail. retry...
		seek = io.SeekStart
		timer := time.NewTimer(openRetryInterval)
		select {
		case <-t.ctx.Done():
			timer.Stop()
			return nil, t.ctx.Err()
		case <-timer.C:
		}
	}
}

func (t *Tail) newReader(r io.Reader) *bufio.Reader {
	const defaultBufSize = 4096
	bufSize := defaultBufSize
	if t.opts.MaxBytesLine != 0 && int64(bufSize) > t.opts.MaxBytesLine {
		bufSize = int(t.opts.MaxBytesLine)
	}
	return bufio.NewReaderSize(r, bufSize)
}

// runFile tails target files
func (t *Tail) runFile(seek int) {
	child, err := t.open(seek)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.errors <- err
		}
		return
	}

	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		child.runFile()
	}()
}

// runFile tails a file
func (t *tail) runFile() {
	defer t.watcher.Close()
	defer t.cancel()

	cherr := make(chan error, 1)
	ch := make(chan struct{}, 1)
	defer close(ch)

	t.parent.wg.Add(1)
	go func() {
		defer t.parent.wg.Done()
		for {
			if err := t.restrict(); err != nil {
				select {
				case cherr <- err:
				case <-t.ctx.Done():
				}
				return
			}
			err := t.tail()
			if err == nil {
				continue
			}

			// wait for writing new lines.
			select {
			case cherr <- err:
			case <-t.ctx.Done():
				return
			}
			select {
			case _, ok := <-ch:
				if !ok {
					return
				}
			case <-t.ctx.Done():
				return
			}
		}
	}()

	var renamed bool
	var waiting bool // waiting for writing new lines?
	for {
		select {
		case event := <-t.watcher.Events:
			if event.Op.Has(fsnotify.Remove) {
				// the target file is removed, stop tailing.
				return
			}
			if event.Op.Has(fsnotify.Rename) {
				// log rotation is detected.
				if !renamed {
					// start to watch creating new file.
					t.parent.wg.Add(1)
					go func() {
						defer t.parent.wg.Done()
						t.parent.runFile(io.SeekStart)
					}()

					// wait a little, and stop tailing old file.
					go func() {
						timer := time.NewTimer(tailOldFileDelay)
						defer timer.Stop()
						select {
						case <-timer.C:
							t.cancel()
						case <-t.ctx.Done():
						}
					}()
				}
				name, err := getFileName(t.file)
				if err != nil {
					t.parent.errors <- err
					return
				}
				if err := t.watcher.Add(name); err != nil {
					t.parent.errors <- err
					return
				}
				renamed = true
			}

			// notify new lines are wrote.
			if waiting {
				ch <- struct{}{}
				waiting = false
			}
		case err := <-cherr:
			if errors.Is(err, io.EOF) {
				waiting = true
				continue
			}
			t.parent.errors <- err
			return
		case err := <-t.watcher.Errors:
			t.parent.errors <- err
			return
		case <-t.ctx.Done():
			return
		}
	}
}

// runReader tails io.Reader
func (t *tail) runReader() {
	defer t.cancel()
	err := t.tail()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.parent.errors <- err
		}
		return
	}
}

// restrict detects a file that is truncated
func (t *tail) restrict() error {
	stat, err := t.file.Stat()
	if err != nil {
		return err
	}
	pos, err := t.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if stat.Size() < pos {
		// file is truncated. seek to head of file.
		_, err := t.file.Seek(0, io.SeekStart)
		if err != nil {
			return err
		}
	}
	return nil
}

// tail reads lines until EOF
func (t *tail) tail() error {
	opts := t.parent.opts
	for {
		line, err := t.reader.ReadSlice('\n')
		t.buf.Write(line)
		if errors.Is(err, bufio.ErrBufferFull) {
			// the reader cannot find EOL in its buffer.
			// continue to read a line.
			if opts.MaxBytesLine == 0 || int64(t.buf.Len()) < opts.MaxBytesLine {
				continue
			}
		} else if err != nil {
			return err
		}
		t.parent.lines <- &Line{t.buf.String(), time.Now()}
		t.buf.Reset()
	}
}
