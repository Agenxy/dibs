package blobstore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"github.com/agenxy/dibs/internal/core"
	"github.com/minio/sio"
)

const streamMagic = "DIBSBLB2"

// ErrHashMismatch refuses a declared digest without committing any object.
var ErrHashMismatch = &core.Error{
	Code: "E_HASH_MISMATCH", Msg: "upload does not match its declared sha256",
	Hint: "hash the complete source file and authorize a new upload with that digest",
}

// Upload keeps only a hash state and the encryption library's <=64KiB pending
// segment in memory. Its temp contains ciphertext, never a plaintext spool.
// The owner must serialize Write/Commit/Abort and Abort every abandoned upload.
type Upload struct {
	store     *Store
	file      *os.File
	writer    io.WriteCloser
	hash      hash.Hash
	key       []byte
	size, max int64
	headerLen int
	closed    bool
}

func streamConfig(key []byte) sio.Config {
	return sio.Config{Key: key, MinVersion: sio.Version20, MaxVersion: sio.Version20, CipherSuites: []byte{sio.AES_GCM}}
}

func streamMetadata(key, digest []byte, size int64) []byte {
	out := make([]byte, 72)
	copy(out, key)
	copy(out[32:64], digest)
	binary.BigEndian.PutUint64(out[64:], uint64(size)) //nolint:gosec // G115: accepted nonnegative upload offset
	return out
}

// BeginUpload allocates an encrypted, mode-0600 staging file. Tickets and quota
// admission belong to the transfer manager, not this off-thread byte adapter.
func (s *Store) BeginUpload(maxSize int64) (*Upload, error) {
	if maxSize < 0 {
		return nil, ErrTooLarge
	}
	u := &Upload{store: s, key: make([]byte, 32), hash: sha256.New(), max: maxSize}
	if _, err := rand.Read(u.key); err != nil {
		return nil, err
	}
	sealed, err := s.box.SealBytes(streamMetadata(u.key, make([]byte, 32), 0))
	if err != nil {
		return nil, err
	}
	u.headerLen = len(sealed)
	u.file, err = os.CreateTemp(s.blobsDir, tmpPrefix+"upload-*")
	if err != nil {
		return nil, err
	}
	prefix := make([]byte, 12)
	copy(prefix, streamMagic)
	binary.BigEndian.PutUint32(prefix[8:], uint32(len(sealed))) //nolint:gosec // G115: fixed 100-byte wrapped metadata
	if _, err = u.file.Write(append(prefix, sealed...)); err == nil {
		// Encryption Close finalizes the stream, but must not close our file:
		// the authenticated envelope still has to be rewritten and fsynced.
		u.writer, err = sio.EncryptWriter(struct{ io.Writer }{u.file}, streamConfig(u.key))
	}
	if err != nil {
		u.Abort()
		return nil, err
	}
	return u, nil
}

// Write hashes exactly the bytes accepted by the authenticated encrypting
// writer. A read-side connection error does not finalize or discard this state.
func (u *Upload) Write(p []byte) (int, error) {
	if u.closed {
		return 0, os.ErrClosed
	}
	if int64(len(p)) > u.max-u.size {
		return 0, ErrTooLarge
	}
	n, err := u.writer.Write(p)
	_, _ = u.hash.Write(p[:n])
	u.size += int64(n)
	return n, err
}

// Size is the accepted plaintext offset, including the pending encrypted chunk.
func (u *Upload) Size() int64 { return u.size }

// Abort removes only this constructor-created ciphertext temp. No registry
// entry exists yet, and keys and buffering become unreachable with the ticket.
func (u *Upload) Abort() {
	if u.closed {
		return
	}
	u.closed = true
	_ = u.file.Close()
	_ = os.Remove(u.file.Name())
	clear(u.key)
}

// Commit finalizes encryption and authenticates the key, digest and length under
// the daemon key. It holds the resulting ID across rename until registration;
// callers MUST Release(id) after the existing ledgered blob op settles.
func (u *Upload) Commit(expectedHex string) (string, int64, error) {
	if u.closed {
		return "", 0, os.ErrClosed
	}
	digest := u.hash.Sum(nil)
	if expectedHex != "" && expectedHex != hex.EncodeToString(digest) {
		u.Abort()
		return "", 0, ErrHashMismatch
	}
	id := "sha256:" + hex.EncodeToString(digest)
	if err := u.writer.Close(); err != nil {
		u.Abort()
		return "", 0, err
	}
	sealed, err := u.store.box.SealBytes(streamMetadata(u.key, digest, u.size))
	if err != nil || len(sealed) != u.headerLen {
		u.Abort()
		if err == nil {
			err = errors.New("stream metadata encryption changed size")
		}
		return "", 0, err
	}
	if _, err = u.file.WriteAt(sealed, 12); err == nil {
		err = u.file.Sync()
	}
	if err == nil {
		err = u.file.Close()
	}
	if err != nil {
		u.Abort()
		return "", 0, err
	}
	dst, err := u.store.blobPath(id)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(dst), 0o700)
	}
	u.store.hold(id)
	if err == nil {
		err = os.Rename(u.file.Name(), dst)
	}
	if err == nil {
		err = fsyncDir(filepath.Dir(dst))
	}
	if err != nil {
		u.store.Release(id)
		u.Abort()
		return "", 0, err
	}
	u.closed = true
	clear(u.key)
	return id, u.size, nil
}

// BlobReader decrypts authenticated chunks lazily and supports Range without
// decoding the entire new-format object. Legacy single-shot GCM retains its
// existing bounded whole-object authentication requirement.
type BlobReader struct {
	*io.SectionReader
	file *os.File
}

// Close releases the encrypted object handle, not its registry reference.
func (r *BlobReader) Close() error { return r.file.Close() }

// Open validates the ID, authenticated envelope, physical length and final
// segment before any caller can send HTTP headers or serve a prefix Range.
func (s *Store) Open(id string) (*BlobReader, error) {
	path, err := s.blobPath(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // G304: validated content-addressed path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMissing
		}
		return nil, err
	}
	r, err := s.openFile(f, id)
	if err != nil {
		_ = f.Close()
	}
	return r, err
}

func (s *Store) openFile(f *os.File, id string) (*BlobReader, error) {
	prefix := make([]byte, 12)
	n, err := f.ReadAt(prefix, 0)
	if n < 8 || string(prefix[:8]) != streamMagic {
		return s.openLegacy(f, id)
	}
	if err != nil {
		return nil, err
	}
	return s.openStream(f, id, prefix)
}

func (s *Store) openStream(f *os.File, id string, prefix []byte) (*BlobReader, error) {
	length := binary.BigEndian.Uint32(prefix[8:])
	if length == 0 || length > 1024 {
		return nil, errors.New("invalid streaming envelope length")
	}
	sealed := make([]byte, length)
	if _, err := f.ReadAt(sealed, 12); err != nil {
		return nil, err
	}
	meta, err := s.box.OpenBytes(sealed)
	if err != nil {
		return nil, fmt.Errorf("invalid streaming envelope: %w", err)
	}
	if len(meta) != 72 {
		return nil, errors.New("invalid streaming envelope metadata length")
	}
	if "sha256:"+hex.EncodeToString(meta[32:64]) != id {
		return nil, errors.New("streaming envelope digest mismatch")
	}
	size := binary.BigEndian.Uint64(meta[64:])
	encSize, err := sio.EncryptedSize(size)
	if err != nil {
		return nil, err
	}
	if size > 1<<48 || encSize > 1<<49 {
		return nil, errors.New("streaming object exceeds format bounds")
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(12 + length)
	if fi.Size() != offset+int64(encSize) {
		return nil, errors.New("truncated or extended streaming object")
	}
	reader, err := sio.DecryptReaderAt(io.NewSectionReader(f, offset, int64(encSize)), streamConfig(meta[:32]))
	if err != nil {
		return nil, err
	}
	if size > 0 {
		if _, err = reader.ReadAt(make([]byte, 1), int64(size)-1); err != nil {
			return nil, err // verify authenticated final segment even for prefix ranges
		}
	}
	return &BlobReader{SectionReader: io.NewSectionReader(reader, 0, int64(size)), file: f}, nil
}

func (s *Store) openLegacy(f *os.File, id string) (*BlobReader, error) {
	sealed, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	plain, err := s.box.OpenBytes(sealed)
	if err != nil {
		return nil, err
	}
	if idOf(plain) != id {
		return nil, errors.New("legacy blob digest mismatch")
	}
	return &BlobReader{SectionReader: io.NewSectionReader(bytes.NewReader(plain), 0, int64(len(plain))), file: f}, nil
}
