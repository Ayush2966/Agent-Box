package proc

import (
	"fmt"
	"os"
	"path/filepath"
)

// rotatingFile appends to path and rotates it to path.1..path.<keep> once it exceeds max bytes.
type rotatingFile struct {
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

func openRotating(path string, max int64, keep int) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotatingFile{path: path, max: max, keep: keep, f: f, size: st.Size()}, nil
}

func (w *rotatingFile) Write(p []byte) (int, error) {
	if w.size > 0 && w.size+int64(len(p)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingFile) rotate() error {
	w.f.Close()
	for i := w.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	_ = os.Rename(w.path, w.path+".1")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	return nil
}

func (w *rotatingFile) Close() error { return w.f.Close() }
