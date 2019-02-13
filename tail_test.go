package tail

import (
	"bufio"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
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
	actual, err := recieve(t, tail)
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
	actual, err := recieve(t, tail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if actual != expected {
		t.Errorf("got %s\nwant %s", actual, expected)
	}

	reader.Close()
	writer.Close()
	tail.Close()
}

func writeFile(t *testing.T, tmpdir string) error {
	time.Sleep(2 * time.Second) // wait for start Tail...

	filename := filepath.Join(tmpdir, "test.log")
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	for _, line := range Logs {
		_, err := file.WriteString(line)
		if err != nil {
			return err
		}
		t.Logf("write: %s", line)
		switch line {
		case RotateMarker:
			file.Close()
			os.Rename(filename, filename+".old")
			file, _ = os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
		case TruncateMarker:
			time.Sleep(1 * time.Second)
			os.Truncate(filename, 0)
			file.Seek(int64(0), os.SEEK_SET)
		}
		time.Sleep(1 * time.Millisecond)
	}

	file.Close()
	return nil
}

func writeWriter(t *testing.T, writer io.Writer) error {
	time.Sleep(2 * time.Second) // wait for start Tail...

	w := bufio.NewWriter(writer)
	for _, line := range Logs {
		_, err := w.WriteString(line)
		if err != nil {
			return err
		}
		w.Flush()
		t.Logf("write: %s", line)
		time.Sleep(1 * time.Millisecond)
	}
	return nil
}

func recieve(t *testing.T, tail *Tail) (string, error) {
	actual := ""
	for {
		select {
		case line := <-tail.Lines:
			t.Logf("recieved: %s", line.Text)
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
