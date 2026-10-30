// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package localtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Supported reports an implemented, kernel-authenticated local transport.
func Supported() bool { return true }

func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(st.Uid) == uint64(os.Geteuid()) // #nosec G115 -- Unix euid is nonnegative.
}

// CheckDir checks the final directory through a no-follow descriptor. Ancestor
// spelling aliases are permitted; the connected peer is verified separately.
func CheckDir(dir string, create bool) error {
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open private data directory %s without following a symlink: %w; "+
			"create a directory owned by this user with mode 0700", dir, err)
	}
	f := os.NewFile(uintptr(fd), dir)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || !owned(info) || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("data directory %s must be owned by this user with mode 0700; "+
			"repair its ownership and permissions before starting dibd or sending credentials", dir)
	}
	return checkAncestors(dir, info)
}

// A private final directory is insufficient if another OS user can rename it
// from its parent. Root/current-user ownership and non-writable ancestors (or
// a trusted sticky directory such as /tmp) make the pathname stable against
// the other-UID adversary while ordinary path I/O opens this profile's files.
func checkAncestors(dir string, final os.FileInfo) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return err
	}
	current, err := os.Stat(resolved)
	if err != nil || !os.SameFile(final, current) {
		return errors.New("data directory changed during validation; inspect its parent and retry")
	}
	for parent := filepath.Dir(resolved); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		trustedOwner := ok && (st.Uid == 0 || uint64(st.Uid) == uint64(os.Geteuid())) // #nosec G115 -- euid is nonnegative.
		if !info.IsDir() || !trustedOwner || (info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("data directory ancestor %s can be replaced by another OS user; "+
				"move this profile beneath your own private home or a root-owned sticky temporary directory", parent)
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

func checkSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &notListening{socketError(path, err)}
		}
		return socketError(path, err)
	}
	if info.Mode()&os.ModeSocket == 0 || !owned(info) || info.Mode().Perm() != 0o600 {
		return socketError(path, errors.New("endpoint must be a socket owned by this user with mode 0600"))
	}
	return nil
}

// Dial verifies filesystem authority and then the actual connected peer before
// the returned connection can carry headers, bodies, tickets or credentials.
func Dial(ctx context.Context, dir string) (net.Conn, error) {
	if err := CheckDir(dir, false); err != nil {
		return nil, err
	}
	path := Path(dir)
	if err := checkSocket(path); err != nil {
		return nil, err
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, socketError(path, err)
	}
	if err := verifyPeer(conn); err != nil {
		_ = conn.Close()
		return nil, socketError(path, err)
	}
	return conn, nil
}

func verifyPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("local IPC requires a Unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var peerErr error
	// #nosec G115 -- a live socket fd fits int.
	if err := raw.Control(func(fd uintptr) { peerErr = peerIsUser(int(fd)) }); err != nil {
		return err
	}
	return peerErr
}

// Listen never steals an existing live endpoint. A stale owned socket is
// removed only after a refused connection and an unchanged inode check.
func Listen(dir string) (net.Listener, error) {
	if err := CheckDir(dir, true); err != nil {
		return nil, err
	}
	path := Path(dir)
	old, err := os.Lstat(path)
	if err == nil {
		if err := checkSocket(path); err != nil {
			return nil, err
		}
		c, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			_ = c.Close()
			return nil, fmt.Errorf("local IPC %s is already serving; stop this data directory's existing daemon first", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, socketError(path, dialErr)
		}
		now, statErr := os.Lstat(path)
		if statErr != nil || !os.SameFile(old, now) {
			return nil, socketError(path, errors.New("endpoint changed while checking it; inspect the data directory and retry"))
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("bind local IPC %s: %w; choose a shorter private data-directory path "+
			"if the Unix socket pathname is too long", path, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &listener{UnixListener: ln, path: path, inode: info}, nil
}

type listener struct {
	*net.UnixListener
	path  string
	inode os.FileInfo
}

func (l *listener) Close() error {
	err := l.UnixListener.Close()
	if now, e := os.Lstat(l.path); e == nil && os.SameFile(l.inode, now) {
		_ = os.Remove(l.path)
	}
	return err
}
