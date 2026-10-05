package proc

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

// Log is a box's output sink: a rotated file on disk plus an in-memory ring for live followers.
type Log struct {
	mu   sync.Mutex
	Ring *Ring
	file *rotatingFile
}

func OpenLog(path string) (*Log, error) {
	f, err := openRotating(path, 10<<20, 3)
	if err != nil {
		return nil, err
	}
	return &Log{Ring: NewRing(2000), file: f}, nil
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(p); err != nil {
		return 0, err
	}
	return l.Ring.Write(p)
}

func (l *Log) Close() error {
	l.Ring.CloseSubs()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// TailFile returns up to the last n lines of the log file at path (reading at most 1 MB).
func TailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const window = 1 << 20
	off := max(0, st.Size()-window)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if off > 0 {
		lines = lines[1:] // first line is probably cut in half
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
