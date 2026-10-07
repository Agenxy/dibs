// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package blobstore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func stagedStream(t *testing.T, s *Store, plain []byte) string {
	t.Helper()
	u, err := s.BeginUpload(int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.Abort)
	for offset := 0; offset < len(plain); {
		end := min(offset+17013, len(plain))
		if _, err := u.Write(plain[offset:end]); err != nil {
			t.Fatal(err)
		}
		offset = end
	}
	id, size, err := u.Commit("")
	if err != nil || size != int64(len(plain)) {
		t.Fatalf("commit size=%d err=%v", size, err)
	}
	s.Release(id)
	return id
}

func TestStreamRoundTripAndLegacy(t *testing.T) {
	for _, size := range []int{0, 1, 65535, 65536, 65537, 200000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s, _ := newStore(t)
			plain := bytes.Repeat([]byte{0x37}, size)
			id := stagedStream(t, s, plain)
			got, err := s.Read(id)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("read err=%v len=%d", err, len(got))
			}
			r, err := s.Open(id)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			if size > 65537 {
				buf := make([]byte, 100)
				if _, err := r.ReadAt(buf, 65530); err != nil || !bytes.Equal(buf, plain[65530:65630]) {
					t.Fatalf("cross-chunk range: %v", err)
				}
			}
			path, err := s.Materialize(id)
			if err != nil {
				t.Fatal(err)
			}
			got, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("materialize: %v", err)
			}
		})
	}
	s, _ := newStore(t)
	id, _, err := s.Put([]byte("legacy one-shot GCM"), cap)
	if err != nil {
		t.Fatal(err)
	}
	s.Release(id)
	if got, err := s.Read(id); err != nil || string(got) != "legacy one-shot GCM" {
		t.Fatalf("legacy: %v", err)
	}
}

func TestStreamStagingEncryptedAndBounded(t *testing.T) {
	s, _ := newStore(t)
	u, err := s.BeginUpload(200000)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Abort()
	secret := bytes.Repeat([]byte("distinct plaintext that must never appear on disk!"), 3000)
	if _, err := u.Write(secret); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(u.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, secret[:100]) || len(raw) < 65536 {
		t.Fatal("staging is plaintext or failed to flush full chunks")
	}
	fi, err := u.file.Stat()
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("staging mode: %v", err)
	}
	if _, err := u.Write(make([]byte, 200000)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("cap: %v", err)
	}
	if _, _, err := u.Commit("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("digest: %v", err)
	}
	if _, err := os.Stat(u.file.Name()); !os.IsNotExist(err) {
		t.Fatalf("rejected staging file retained: %v", err)
	}
}

func TestStreamRejectsCorruptionAndMissingFinalSegment(t *testing.T) {
	for _, mutation := range []string{"header", "middle", "reorder", "final-bit", "truncate-final", "append"} {
		t.Run(mutation, func(t *testing.T) {
			s, _ := newStore(t)
			plain := bytes.Repeat([]byte("nontrivial data!"), 15000)
			id := stagedStream(t, s, plain)
			path, err := s.blobPath(id)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			body := 12 + int(binary.BigEndian.Uint32(raw[8:12]))
			const segment = 65536 + 32
			switch mutation {
			case "header":
				raw[16] ^= 1
			case "middle":
				raw[body+100] ^= 1
			case "reorder":
				first := append([]byte(nil), raw[body:body+segment]...)
				copy(raw[body:body+segment], raw[body+segment:body+2*segment])
				copy(raw[body+segment:body+2*segment], first)
			case "final-bit":
				raw[body+3*segment+1] ^= 0x80
			case "truncate-final":
				raw = raw[:body+3*segment]
			case "append":
				raw = append(raw, 1)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			r, err := s.Open(id)
			if mutation == "middle" || mutation == "reorder" {
				if err == nil {
					defer func() { _ = r.Close() }()
					_, err = io.ReadAll(r)
				}
			} else if r != nil {
				_ = r.Close()
			}
			if err == nil {
				t.Fatal("tampered/truncated stream was served successfully")
			}
		})
	}
}
