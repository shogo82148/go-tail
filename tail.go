package tail

import (
	"bufio"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	fsnotify "gopkg.in/fsnotify.v1"
)

const (
	openRetryInterval = time.Second
	tailOldFileDelay  = 10 * time.Second
)

var errDone = errors.New("tail: done event loop")

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
}

type tail struct {
	parent *Tail

	file    *os.File
	reader  *bufio.Reader
	watcher *fsnotify.Watcher
	buf     string
}

// NewTailFile starts tailing a file
func NewTailFile(filename string) (*Tail, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}

	lines := make(chan *Line, 16)
	errs := make(chan error, 1)
	parent := &Tail{
		Lines:    lines,
		Errors:   errs,
		lines:    lines,
		errors:   errs,
		filename: filename,
	}
	go parent.runFile(os.SEEK_END)
	return parent, nil
}

// NewTailReader starts tailing io.Reader
func NewTailReader(reader io.Reader) (*Tail, error) {
	lines := make(chan *Line, 16)
	errs := make(chan error, 1)
	parent := &Tail{
		Lines:  lines,
		Errors: errs,
		lines:  lines,
		errors: errs,
	}
	t := &tail{
		parent: parent,
		reader: bufio.NewReader(reader),
	}
	go t.runReader()
	return parent, nil
}

// Close stops tailing the file.
func (t *Tail) Close() error {
	// TODO: stop tailing
	return nil
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
			return &tail{
				parent:  t,
				file:    file,
				reader:  bufio.NewReader(file),
				watcher: watcher,
			}, nil
		}

		// fail. retry...
		seek = os.SEEK_SET
		select {
		case <-time.After(openRetryInterval):
		}
	}
}

// runFile tails target files
func (t *Tail) runFile(seek int) {
	child, err := t.open(seek)
	if err != nil {
		t.errors <- err
		return
	}
	go child.runFile()
}

// runFile tails a file
func (t *tail) runFile() {
	defer log.Println(t, "stopped")
	ch := make(chan struct{}, 100)
	var renamed bool
	go func() {
		for {
			if err := t.restict(); err != nil {
				t.parent.errors <- err
				break
			}
			err := t.tail()
			if err == nil {
				continue
			}
			if err != io.EOF {
				t.parent.errors <- err
				break
			}
			if _, ok := <-ch; !ok {
				break
			}
		}
	}()
	for {
		select {
		case event := <-t.watcher.Events:
			log.Println(event.Name, event.Op)
			if (event.Op & fsnotify.Remove) != 0 {
				// the target file is removed, stop tailing.
				return
			}
			if (event.Op & fsnotify.Rename) != 0 {
				// log rotation is detected.
				if !renamed {
					// start to watch creating new file.
					go t.parent.runFile(os.SEEK_SET)
				}
				t.file.Name()
				renamed = true
			}
			// notify write event
			select {
			case ch <- struct{}{}:
			default:
			}
		case err := <-t.watcher.Errors:
			t.parent.errors <- err
			return
		}
	}
}

// runReader tails io.Reader
func (t *tail) runReader() {
	err := t.tail()
	if err == errDone || err == io.EOF || err == io.ErrClosedPipe {
		return
	}
	if err != nil {
		t.parent.errors <- err
		return
	}
}

// restrict detects a file that is truncated
func (t *tail) restict() error {
	stat, err := t.file.Stat()
	if err != nil {
		return err
	}
	pos, err := t.file.Seek(0, os.SEEK_CUR)
	if err != nil {
		return err
	}
	if stat.Size() < pos {
		// file is trancated. seek to head of file.
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
		log.Println(t.buf + line)
		t.parent.lines <- &Line{t.buf + line, time.Now()}
		t.buf = ""
	}
}
