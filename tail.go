package tail

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/fsnotify.v1"
)

const (
	OpenRetryInterval = 1 * time.Second
)

type Line struct {
	Text string
	Time time.Time
}

type Tail struct {
	Lines  chan *Line
	Errors chan error

	filename string
	file     *os.File
	reader   *bufio.Reader
	watcher  *fsnotify.Watcher
	buf      string
	done     chan bool
	doneFlag bool
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
		done:     make(chan bool),
	}
	go t.runFile()

	return t, nil
}

// NewTailReader starts tailing io.Reader
func NewTailReader(reader io.Reader) (*Tail, error) {
	t := &Tail{
		Lines:  make(chan *Line),
		Errors: make(chan error),
		reader: bufio.NewReader(reader),
		done:   make(chan bool),
	}
	go t.runReader()
	return t, nil
}

func (t *Tail) Close() error {
	t.doneFlag = true
	t.done <- true
	close(t.done)
	return nil
}

func (t *Tail) open(seek int) {
	for {
		fin, err := os.Open(t.filename)
		if err == nil {
			// success
			fin.Seek(0, seek)
			t.file = fin
			t.reader = bufio.NewReader(fin)
			t.watcher.Add(t.filename)
			return
		}

		// fail. retry...
		seek = os.SEEK_SET
		select {
		case <-t.done:
			return
		case <-time.After(OpenRetryInterval):
		}
	}
}

// runFile tails a file
func (t *Tail) runFile() {
	t.open(os.SEEK_END)
	for {
		if err := t.eventLoop(); err != nil && !t.doneFlag {
			t.Errors <- err
		}
		if t.doneFlag {
			break
		}
		t.open(os.SEEK_SET)
	}

	// Tail is closed. cleanup
	t.watcher.Remove(t.filename)
	t.watcher.Close()
	close(t.Lines)
	close(t.Errors)

	// flush done chan
	<-t.done
}

// runReader tails io.Reader
func (t *Tail) runReader() {
	if err := t.tail(); err != nil && !t.doneFlag {
		t.Errors <- err
	}

	// Tail is closed. cleanup
	close(t.Lines)
	close(t.Errors)

	// flush done chan
	<-t.done
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
	for !t.doneFlag {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			t.buf += line
			return err
		}
		t.Lines <- &Line{t.buf + line, time.Now()}
		t.buf = ""
	}
	return nil
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
			return nil
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
