// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type aclPurpose uint8

const (
	aclAncestor aclPurpose = iota
	aclPrivateDirectory
	aclCredential
	aclAuthority
)

// Descriptor metadata only: no authorization API, Directory Services request,
// subprocess or TCC prompt. Callers verify ownership first, so doctor never
// enters this path for another user's private files.
func checkDirectoryACL(f *os.File, private bool) error {
	purpose := aclAncestor
	if private {
		purpose = aclPrivateDirectory
	}
	return checkDescriptorACL(f, purpose)
}

func checkCredentialDescriptorACL(f *os.File) error { return checkDescriptorACL(f, aclCredential) }

func checkAncestorACL(path string, info os.FileInfo) error {
	return checkPathACL(path, info, aclAncestor)
}

func checkCredentialACL(path string, info os.FileInfo) error {
	return checkPathACL(path, info, aclCredential)
}

func checkAuthorityACL(path string, info os.FileInfo) error {
	return checkPathACL(path, info, aclAuthority)
}

func checkPathACL(path string, expected os.FileInfo, purpose aclPurpose) error {
	fd, err := unix.Open(path, unix.O_EVTONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return aclReview(path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(expected, actual) {
		return aclReview(path, errors.New("object changed before descriptor ACL inspection"))
	}
	return checkDescriptorACL(f, purpose)
}

func checkDescriptorACL(f *os.File, purpose aclPurpose) error {
	attrs := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY | unix.ATTR_CMN_UUID}
	// SDK sys/kauth.h limits ACLs to 128 ACEs. Fixed attributes plus file
	// security and 128*24 ACE bytes fit in this bounded kernel-output buffer.
	buffer := make([]byte, 4096)
	// Like paths' F_GETPATH, this native descriptor operation has no pure-Go
	// libSystem wrapper. Kernel output is bounded and validated below.
	// #nosec G103 -- fixed syscall, live fixed-size attrs and bounded live output buffer
	_, _, errno := syscall.Syscall6(syscall.SYS_FGETATTRLIST, f.Fd(), uintptr(unsafe.Pointer(&attrs)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), unix.FSOPT_REPORT_FULLSIZE, 0)
	if errno != 0 {
		return aclReview(f.Name(), errno)
	}
	if err := inspectDarwinACL(buffer, purpose); err != nil {
		return aclReview(f.Name(), err)
	}
	return nil
}

func inspectDarwinACL(b []byte, purpose aclPurpose) error {
	if len(b) < 28 {
		return errors.New("short descriptor attributes")
	}
	size := int64(binary.NativeEndian.Uint32(b[:4]))
	offset := int64(binary.NativeEndian.Uint32(b[4:8]))
	if offset >= 1<<31 {
		offset -= 1 << 32 // attrreference_t carries a signed 32-bit relative offset.
	}
	offset += 4
	length := int64(binary.NativeEndian.Uint32(b[8:12]))
	if size < 28 || size > int64(len(b)) || offset < 28 || offset > size || length > size-offset {
		return errors.New("invalid descriptor ACL bounds")
	}
	if length == 0 {
		return nil
	}
	acl := b[offset : offset+length]
	if len(acl) < 44 || binary.NativeEndian.Uint32(acl[:4]) != 0x012cc16d {
		return errors.New("unknown descriptor file-security format")
	}
	count := uint64(binary.NativeEndian.Uint32(acl[36:40]))
	if count > 128 || uint64(len(acl)) != 44+count*24 {
		return errors.New("invalid descriptor ACE count")
	}
	owner := b[12:28]
	ownerKnown := !bytes.Equal(owner, make([]byte, 16))
	return inspectDarwinEntries(acl[44:], owner, ownerKnown, purpose)
}

func inspectDarwinEntries(entries, owner []byte, ownerKnown bool, purpose aclPurpose) error {
	for entry := entries; len(entry) > 0; entry = entry[24:] {
		flags := binary.NativeEndian.Uint32(entry[16:20])
		rights := binary.NativeEndian.Uint32(entry[20:24])
		if unknownACLEvidence(flags, rights) {
			return errors.New("unknown ACL flags or rights")
		}
		switch flags & 0xf {
		case 2: // KAUTH_ACE_DENY: retained, never deleted or widened.
			continue
		case 1: // KAUTH_ACE_PERMIT, including KAUTH_ACE_INHERITED.
			if ownerKnown && bytes.Equal(entry[:16], owner) {
				continue
			}
			if dangerousACLGrant(flags, rights, purpose) {
				return errors.New("ACL allow grant may expose private data or permit mutation; " +
					"owner UUID evidence may be unavailable")
			}
		default:
			return errors.New("unknown ACL entry kind")
		}
	}
	return nil
}

func dangerousACLGrant(flags, rights uint32, purpose aclPurpose) bool {
	// SDK sys/kauth.h: add/write, append/subdirectory, delete, delete-child,
	// attributes, extended attributes, security and ownership-changing rights.
	write := uint32(1<<2 | 1<<4 | 1<<5 | 1<<6 | 1<<8 | 1<<10 | 1<<12 | 1<<13)
	if purpose == aclAncestor && flags&(1<<8) != 0 {
		return false // inherit-only ACE cannot mutate this ancestor itself.
	}
	mask := write | 1<<21 | 1<<23
	if purpose == aclCredential || purpose == aclPrivateDirectory {
		// Root search grants could expose independent config/CA file ACLs.
		// Inherit-only grants count here too: future secrets must stay private.
		mask |= 1<<1 | 1<<3 | 1<<22 | 1<<24
	}
	return rights&mask != 0
}

func unknownACLEvidence(flags, rights uint32) bool {
	generic := uint32(1<<21 | 1<<22 | 1<<23 | 1<<24)
	return rights&^(uint32(0x3ffe)|generic|1<<20) != 0 || flags&^uint32(0x1ff) != 0
}

func aclReview(path string, err error) error {
	return fmt.Errorf("cannot establish private ACL access for %s: %w; run ls -le %s and review its grants; "+
		"chmod -N %s is an operator choice after that review, never an automatic repair",
		path, err, literalPath(path), literalPath(path))
}
