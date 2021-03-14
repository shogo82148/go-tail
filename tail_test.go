package tail

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	RotateMarker   = "__ROTATE__\n"
	TruncateMarker = "__TRUNCATE__\n"
	EOFMarker      = "__EOF__\n"
)

var Logs = []string{
	"single line\n",
	"multi line 1\nmulti line 2\nmulti line 3\n",
	"continuous line 1", "continuous line 2", "continuous line 3\n",
	RotateMarker,
	"foo\n",
	"bar\n",
	"baz\n",
	TruncateMarker,
	"FOOOO\n",
	"BAAAR\n",
	"BAZZZZZZZ\n",
	EOFMarker,
}

func TestTailFile(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "go-tail.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.RemoveAll(tmpdir)

	go writeFile(t, tmpdir)
	tail, err := NewTailFile(filepath.Join(tmpdir, "test.log"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer tail.Close()

	expected := strings.Join(Logs, "")
	actual, err := receive(t, tail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actual != expected {
		t.Errorf("got %s\nwant %s", actual, expected)
	}
}

func TestTailReader(t *testing.T) {
	reader, writer := io.Pipe()

	go writeWriter(t, writer)
	tail, err := NewTailReader(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := strings.Join(Logs, "")
	actual, err := receive(t, tail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actual != expected {
		t.Errorf("got %s\nwant %s", actual, expected)
	}

	reader.Close()
	writer.Close()
	select {
	case _, ok := <-tail.Lines:
		if ok {
			t.Error("want closed, but not")
		}
	case <-time.After(time.Second):
		t.Error("want closed, but not")
	}
	select {
	case _, ok := <-tail.Errors:
		if ok {
			t.Error("want closed, but not")
		}
	case <-time.After(time.Second):
		t.Error("want closed, but not")
	}
	tail.Close()
}

func writeFile(t *testing.T, tmpdir string) error {
	filename := filepath.Join(tmpdir, "test.log")
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	// wait for starting to tail...
	time.Sleep(2 * time.Second)

	for _, line := range Logs {
		_, err := file.WriteString(line)
		if err != nil {
			return err
		}
		t.Logf("write: %s", line)
		switch line {
		case RotateMarker:
			if err := file.Close(); err != nil {
				return err
			}
			if err := os.Rename(filename, filename+".old"); err != nil {
				return err
			}
			file, err = os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
		case TruncateMarker:
			time.Sleep(1 * time.Second)
			if _, err := file.Seek(0, os.SEEK_SET); err != nil {
				return err
			}
			if err := os.Truncate(filename, 0); err != nil {
				return err
			}
		}
		time.Sleep(90 * time.Millisecond)
	}

	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

func writeWriter(t *testing.T, writer io.Writer) error {
	w := bufio.NewWriter(writer)
	for _, line := range Logs {
		_, err := w.WriteString(line)
		if err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
		t.Logf("write: %s", line)
		time.Sleep(90 * time.Millisecond)
	}
	return nil
}

func receive(t *testing.T, tail *Tail) (string, error) {
	actual := ""
	for {
		select {
		case line := <-tail.Lines:
			t.Logf("received: %s", line.Text)
			actual += line.Text
			if line.Text == EOFMarker {
				return actual, nil
			}
		case err := <-tail.Errors:
			return "", err
		case <-time.After(5 * time.Second):
			return "", errors.New("timeout")
		}
	}
}

func TestTailFile_Rotate(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "go-tail.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.RemoveAll(tmpdir)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		filename := filepath.Join(tmpdir, "test.log")
		for i := 0; i < 10; i++ {
			file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				t.Error(err)
				return
			}
			if i == 0 {
				// wait for starting to tail...
				time.Sleep(2 * time.Second)
			}

			// start to write logs
			wg.Add(1)
			go func() {
				defer wg.Done()
				writeFileAndClose(t, file, fmt.Sprintf("file: %d\n", i))
			}()
			time.Sleep(time.Second)

			// Rotate log file, and start writing logs into a new file.
			// While, some logs are still written into the old file.
			os.Rename(filename, fmt.Sprintf("%s.%d", filename, i))
		}
	}()

	tail, err := NewTailFile(filepath.Join(tmpdir, "test.log"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	go func() {
		wg.Wait()
		tail.Close()
	}()
	go func() {
		for err := range tail.Errors {
			t.Log("error: ", err)
		}
	}()

	var cnt int
	for range tail.Lines {
		cnt++
	}
	if cnt != 1000 {
		t.Errorf("want 1000, got %d", cnt)
	}
}

func writeFileAndClose(t *testing.T, file *os.File, line string) {
	for i := 0; i < 100; i++ {
		_, err := file.WriteString(line)
		if err != nil {
			_ = file.Close()
			t.Error(err)
			return
		}
		time.Sleep(90 * time.Millisecond)
	}

	if err := file.Close(); err != nil {
		t.Error(err)
	}
}
