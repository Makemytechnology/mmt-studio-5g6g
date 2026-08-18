// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingFile is a minimal size-based rotating file writer matching Python's
// RotatingFileHandler(maxBytes, backupCount).
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	f        *os.File
	size     int64
}

func newRotatingFile(path string, maxBytes int64, backups int) (*rotatingFile, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingFile{
		path:     path,
		maxBytes: maxBytes,
		backups:  backups,
		f:        f,
		size:     fi.Size(),
	}, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	// Flush each record so `tail -f` shows real-time output (matches Python FlushingRotatingFileHandler).
	_ = r.f.Sync()
	return n, err
}

func (r *rotatingFile) rotateLocked() error {
	_ = r.f.Close()
	for i := r.backups; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", r.path, i-1)
		if i == 1 {
			src = r.path
		}
		dst := fmt.Sprintf("%s.%d", r.path, i)
		_ = os.Remove(dst)
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	r.f = f
	r.size = 0
	return nil
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
