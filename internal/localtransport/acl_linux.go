// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package localtransport

import "os"

// Linux POSIX ACL named users/groups are bounded by the ACL mask, exactly the
// stat group-mode bits. Mode 0700 or 0600 caps every named entry at ---.
// The explicit real setfacl/second-user CI guard measures both that zero-mask
// case and chmod g+rwx exposing the same named-user grant.
func checkDirectoryACL(*os.File, bool) error       { return nil }
func checkAncestorACL(string, os.FileInfo) error   { return nil }
func checkCredentialACL(string, os.FileInfo) error { return nil }
func checkAuthorityACL(string, os.FileInfo) error  { return nil }
func checkCredentialDescriptorACL(*os.File) error  { return nil }
