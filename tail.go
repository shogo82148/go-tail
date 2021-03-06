package tail

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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

	lines    chan<- *Line
	errors   chan<- error
	filename string
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
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

// NewTailFile starts tailing a file
func NewTailFile(filename string) (*Tail, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("tail: failed to get the absolute path: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan *Line, linesCapacity)
	errs := make(chan error, errorsCapacity)
	parent := &Tail{
		Lines:    lines,
		Errors:   errs,
		lines:    lines,
		errors:   errs,
		filename: filename,
		ctx:      ctx,
		cancel:   cancel,
	}
	parent.wg.Add(1)
	go parent.runFile(os.SEEK_END)
	go parent.wait()
	return parent, nil
}

// NewTailReader starts tailing io.Reader
func NewTailReader(reader io.Reader) (*Tail, error) {
	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan *Line, linesCapacity)
	errs := make(chan error, errorsCapacity)
	parent := &Tail{
		Lines:  lines,
		Errors: errs,
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
		reader: bufio.NewReader(r),
		ctx:    ctx,
		cancel: cancel,
	}
	parent.wg.Add(1)
	go t.runReader()
	go parent.wait()
	return parent, nil
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
		return nil, fmt.Errorf("tail: failed to initialize fsnotify: %w", err)
	}
	for {
		file, err := os.Open(t.filename)
		if err == nil {
			// success, seek and watch the file.
			if _, err := file.Seek(0, seek); err != nil {
				file.Close()
				watcher.Close()
				return nil, fmt.Errorf("tail: failed to seek: %w", err)
			}
			if err := watcher.Add(t.filename); err != nil {
				file.Close()
				watcher.Close()
				return nil, fmt.Errorf("tail: failed to watch fsnotify event: %w", err)
			}
			ctx, cancel := context.WithCancel(t.ctx)
			r := ctxReader{
				ctx: ctx,
				r:   file,
			}
			return &tail{
				parent:  t,
				file:    file,
				reader:  bufio.NewReader(r),
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

// runFile tails target files
func (t *Tail) runFile(seek int) {
	defer t.wg.Done()
	child, err := t.open(seek)
	if err != nil {
		if t.ctx.Err() != nil {
			// stopping tailing now. suppress the error.
			return
		}
		t.errors <- err
		return
	}
	t.wg.Add(1)
	go child.runFile()
}

// runFile tails a file
func (t *tail) runFile() {
	defer t.parent.wg.Done()
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
			if (event.Op & fsnotify.Remove) != 0 {
				// the target file is removed, stop tailing.
				return
			}
			if (event.Op & fsnotify.Rename) != 0 {
				// log rotation is detected.
				if !renamed {
					// start to watch creating new file.
					t.parent.wg.Add(1)
					go t.parent.runFile(io.SeekStart)

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
					t.parent.errors <- fmt.Errorf("tail: failed to watch fsnotify event: %w", err)
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
			} else {
				t.parent.errors <- err
				return
			}
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
	defer t.parent.wg.Done()
	defer t.cancel()
	err := t.tail()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return
	}
	if t.ctx.Err() != nil {
		// stopping tailing now. suppress the error.
		return
	}
	if err != nil {
		t.parent.errors <- err
		return
	}
}

// restrict detects a file that is truncated
func (t *tail) restrict() error {
	stat, err := t.file.Stat()
	if err != nil {
		return fmt.Errorf("tail: failed to stat the file: %w", err)
	}
	pos, err := t.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("tail: failed to seek: %w", err)
	}
	if stat.Size() < pos {
		// file is truncated. seek to head of file.
		_, err := t.file.Seek(0, io.SeekStart)
		if err != nil {
			return fmt.Errorf("tail: failed to seek: %w", err)
		}
	}
	return nil
}

// tail reads lines until EOF
func (t *tail) tail() error {
	for {
		line, err := t.reader.ReadSlice('\n')
		t.buf.Write(line)
		if err == bufio.ErrBufferFull {
			// the reader cannot find EOL in its buffer.
			// continue to read a line.
			continue
		}
		if err != nil {
			return fmt.Errorf("tail: failed to read the file: %w", err)
		}
		t.parent.lines <- &Line{t.buf.String(), time.Now()}
		t.buf.Reset()
	}
}
