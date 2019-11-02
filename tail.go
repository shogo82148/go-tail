package tail

import (
	"bufio"
	"context"
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
	buf     string
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewTailFile starts tailing a file
func NewTailFile(filename string) (*Tail, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan *Line, 16)
	errs := make(chan error, 1)
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
	lines := make(chan *Line, 16)
	errs := make(chan error, 1)
	parent := &Tail{
		Lines:  lines,
		Errors: errs,
		lines:  lines,
		errors: errs,
		ctx:    ctx,
		cancel: cancel,
	}
	t := &tail{
		parent: parent,
		reader: bufio.NewReader(reader),
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
			return &tail{
				parent:  t,
				file:    file,
				reader:  bufio.NewReader(file),
				watcher: watcher,
				ctx:     ctx,
				cancel:  cancel,
			}, nil
		}

		// fail. retry...
		seek = os.SEEK_SET
		select {
		case <-t.ctx.Done():
			return nil, t.ctx.Err()
		case <-time.After(openRetryInterval):
		}
	}
}

// runFile tails target files
func (t *Tail) runFile(seek int) {
	defer t.wg.Done()
	child, err := t.open(seek)
	if err != nil {
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

	cherr := make(chan error)
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
					go t.parent.runFile(os.SEEK_SET)

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
				t.watcher.Add(name)
				renamed = true
			}

			// notify new lines are wrote.
			if waiting {
				ch <- struct{}{}
			}
		case err := <-cherr:
			if err == io.EOF {
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
	if err == io.EOF || err == io.ErrClosedPipe {
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
		return err
	}
	pos, err := t.file.Seek(0, os.SEEK_CUR)
	if err != nil {
		return err
	}
	if stat.Size() < pos {
		// file is truncated. seek to head of file.
		_, err := t.file.Seek(0, os.SEEK_SET)
		if err != nil {
			return err
		}
	}
	return nil
}

// tail reads lines until EOF
func (t *tail) tail() error {
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			t.buf += line
			return err
		}
		t.parent.lines <- &Line{t.buf + line, time.Now()}
		t.buf = ""
	}
}
