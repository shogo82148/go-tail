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
	}
	go t.runReader()
	return t, nil
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
		time.Sleep(OpenRetryInterval)
	}
}

// runFile tails a file
func (t *Tail) runFile() {
	t.open(os.SEEK_END)
	for {
		if err := t.eventLoop(); err != nil {
			t.Errors <- err
		}
		t.open(os.SEEK_SET)
	}
}

// runReader tails io.Reader
func (t *Tail) runReader() {
	if err := t.tail(); err != nil {
		t.Errors <- err
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
