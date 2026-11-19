package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// blobPath is where an attachment lives: <dir>/blobs/<aa>/<sha256> (STORAGE §6).
func blobPath(dir, sha string) string {
	return filepath.Join(dir, "blobs", sha[:2], sha)
}

// BlobPath returns the file of a blob. Blobs are immutable.
func (s *Store) BlobPath(sha string) string { return blobPath(s.dir, sha) }

// PutBlob stores the content of r and returns its sha256 and size. Storing the
// same content twice keeps one file.
func (s *Store) PutBlob(r io.Reader) (sha string, size int64, err error) {
	return putBlob(s.dir, r, "")
}

// putBlob writes to a temp file, fsyncs and renames. If want is set, the
// content must have that hash.
func putBlob(dir string, r io.Reader, want string) (sha string, size int64, err error) {
	tmpDir := filepath.Join(dir, "blobs", "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(tmpDir, "blob-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(f.Name()) // no-op after a successful rename
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(f, h), r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("write blob: %w", err)
	}
	sha = hex.EncodeToString(h.Sum(nil))
	if want != "" && sha != want {
		return "", 0, fmt.Errorf("blob %s: content has sha256 %s", want, sha)
	}
	dst := blobPath(dir, sha)
	if _, err := os.Stat(dst); err == nil {
		return sha, size, nil // already stored
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(f.Name(), 0o400); err != nil {
		return "", 0, err
	}
	if err := os.Rename(f.Name(), dst); err != nil {
		return "", 0, err
	}
	return sha, size, syncDir(filepath.Dir(dst))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// hashFile returns the sha256 and size of a file.
func hashFile(path string) (sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	if size, err = io.Copy(h, f); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
