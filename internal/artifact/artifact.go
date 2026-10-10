// Package artifact stores job evidence files, such as captured logs, and
// serves them back for download.
package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// ErrNotFound means no object is stored under the key.
var ErrNotFound = errors.New("artifact not found")

// Object describes stored content.
type Object struct {
	Key    string
	Size   int64
	SHA256 string
}

// Store keeps artifact content under slash-separated keys. *FS implements it.
type Store interface {
	Put(ctx context.Context, key string, data []byte) (Object, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// FS stores artifacts as files below one directory. Keys cannot escape it.
type FS struct {
	root *os.Root
}

// NewFS creates dir if needed and stores artifacts below it.
func NewFS(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create artifact directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open artifact directory: %w", err)
	}
	return &FS{root: root}, nil
}

// Close releases the directory handle.
func (s *FS) Close() error {
	return s.root.Close()
}

// Put writes data under key, replacing any earlier content. The write goes to
// a temporary file that is renamed into place, so readers never see a partial
// file.
func (s *FS) Put(_ context.Context, key string, data []byte) (Object, error) {
	if err := validKey(key); err != nil {
		return Object{}, err
	}
	if dir := path.Dir(key); dir != "." {
		if err := s.root.MkdirAll(dir, 0o750); err != nil {
			return Object{}, fmt.Errorf("create artifact directory: %w", err)
		}
	}
	tmp := key + ".tmp-" + rand.Text()
	if err := s.root.WriteFile(tmp, data, 0o640); err != nil {
		return Object{}, fmt.Errorf("write artifact: %w", err)
	}
	if err := s.root.Rename(tmp, key); err != nil {
		_ = s.root.Remove(tmp)
		return Object{}, fmt.Errorf("store artifact: %w", err)
	}
	sum := sha256.Sum256(data)
	return Object{Key: key, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

// Open returns the content stored under key, or ErrNotFound.
func (s *FS) Open(_ context.Context, key string) (io.ReadCloser, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	f, err := s.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	return f, nil
}

// validKey accepts relative slash-separated keys made of plain path elements.
func validKey(key string) error {
	if key == "" || !fs.ValidPath(key) || strings.ContainsAny(key, `\:`) {
		return fmt.Errorf("invalid artifact key %q", key)
	}
	return nil
}
