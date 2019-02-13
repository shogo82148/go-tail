package tail

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	fsnotify "gopkg.in/fsnotify.v1"
)

const (
	openRetryInterval = time.Second
)

var errDone = errors.New("tail: done event loop")

// Line is a line of the target file.
type Line struct {
	Text string
	Time time.Time
}

// Tail tails a file.
type Tail struct {
	Lines  chan *Line
	Errors chan error

	filename string
	file     *os.File
	reader   *bufio.Reader
	watcher  *fsnotify.Watcher
	buf      string
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewTailFile starts tailing a file
func NewTailFile(filename string) (*Tail, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	watcher.Add(filename)

	t := &Tail{
		Lines:    make(chan *Line),
		Errors:   make(chan error),
		filename: filename,
		watcher:  watcher,
		done:     make(chan struct{}, 1),
	}
	t.wg.Add(1)
	go t.runFile()
	return t, nil
}

// NewTailReader starts tailing io.Reader
func NewTailReader(reader io.Reader) (*Tail, error) {
	t := &Tail{
		Lines:  make(chan *Line),
		Errors: make(chan error),
		reader: bufio.NewReader(reader),
		done:   make(chan struct{}, 1),
	}
	t.wg.Add(1)
	go t.runReader()
	return t, nil
}

// Close stops tailing the file.
func (t *Tail) Close() error {
	t.done <- struct{}{}
	t.wg.Wait()
	return nil
}

func (t *Tail) open(seek int) error {
	for {
		fin, err := os.Open(t.filename)
		if err == nil {
			// success
			fin.Seek(0, seek)
			t.file = fin
			t.reader = bufio.NewReader(fin)
			t.watcher.Add(t.filename)
			return nil
		}

		// fail. retry...
		seek = os.SEEK_SET
		select {
		case <-t.done:
			return errDone
		case <-time.After(openRetryInterval):
		}
	}
}

// runFile tails a file
func (t *Tail) runFile() {
	defer func() {
		// Tail is closed. cleanup
		t.watcher.Remove(t.filename)
		t.watcher.Close()
		close(t.Lines)
		close(t.Errors)
		t.wg.Done()
	}()

	err := t.open(os.SEEK_END)
	if err == errDone {
		return
	}
	if err != nil {
		t.Errors <- err
		return
	}
	for {
		err = t.eventLoop()
		if err == errDone {
			return
		}
		if err != nil {
			t.Errors <- err
			return
		}

		err = t.open(os.SEEK_SET)
		if err == errDone {
			return
		}
		if err != nil {
			t.Errors <- err
			return
		}
	}
}

// runReader tails io.Reader
func (t *Tail) runReader() {
	defer func() {
		// Tail is closed. cleanup
		close(t.Lines)
		close(t.Errors)
		t.wg.Done()
	}()
	err := t.tail()
	if err == errDone || err == io.EOF || err == io.ErrClosedPipe {
		return
	}
	if err != nil {
		t.Errors <- err
		return
	}
}

// restrict detects a file that is truncated
func (t *Tail) restict() error {
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

// Read lines until EOF
func (t *Tail) tail() error {
	for {
		select {
		case <-t.done:
			return errDone
		default:
		}
		line, err := t.reader.ReadString('\n')
		if err != nil {
			t.buf += line
			return err
		}
		t.Lines <- &Line{t.buf + line, time.Now()}
		t.buf = ""
	}
}

func (t *Tail) eventLoop() error {
	defer t.file.Close()
	for {
		err := t.restict()
		if err != nil {
			return err
		}

		err = t.tail()
		if !(err == nil || err == io.EOF) {
			return err
		}

		// wait events
		select {
		case <-t.done:
			return errDone
		case event := <-t.watcher.Events:
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				// watching file is removed. return for reopening.
				return nil
			}
		case err := <-t.watcher.Errors:
			return err
		}
	}
}
