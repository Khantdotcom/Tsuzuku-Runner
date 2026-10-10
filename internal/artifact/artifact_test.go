package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func newTestFS(t *testing.T) (*FS, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewFS(filepath.Join(dir, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestPutAndOpen(t *testing.T) {
	s, _ := newTestFS(t)
	data := []byte("hello\n")

	obj, err := s.Put(t.Context(), "job/attempt/stdout.log", data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if obj.Size != 6 || obj.SHA256 != hex.EncodeToString(sum[:]) || obj.Key != "job/attempt/stdout.log" {
		t.Errorf("object = %+v", obj)
	}

	r, err := s.Open(t.Context(), "job/attempt/stdout.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "hello\n" {
		t.Errorf("content = %q, err %v", got, err)
	}
}

func TestPutReplacesAndLeavesNoTempFiles(t *testing.T) {
	s, dir := newTestFS(t)
	if _, err := s.Put(t.Context(), "a/b.log", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), "a/b.log", []byte("second")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "artifacts", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "b.log" {
		t.Errorf("directory holds %v, want only b.log", entries)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "artifacts", "a", "b.log"))
	if string(got) != "second" {
		t.Errorf("content = %q", got)
	}
}

func TestEmptyContent(t *testing.T) {
	s, _ := newTestFS(t)
	obj, err := s.Put(t.Context(), "empty.log", nil)
	if err != nil || obj.Size != 0 || obj.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("object = %+v, err %v", obj, err)
	}
}

func TestOpenMissing(t *testing.T) {
	s, _ := newTestFS(t)
	if _, err := s.Open(t.Context(), "nope.log"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestKeysCannotEscapeTheDirectory(t *testing.T) {
	s, dir := newTestFS(t)
	for _, key := range []string{"../outside.log", "/abs.log", "a/../../x", `a\b`, "C:/x", "", "a//b", "./a"} {
		if _, err := s.Put(t.Context(), key, []byte("x")); err == nil {
			t.Errorf("Put(%q) succeeded", key)
		}
		if _, err := s.Open(t.Context(), key); err == nil {
			t.Errorf("Open(%q) succeeded", key)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.log")); err == nil {
		t.Error("a file was written outside the artifact directory")
	}
}
