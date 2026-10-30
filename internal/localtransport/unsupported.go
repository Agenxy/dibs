// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package localtransport

import (
	"context"
	"errors"
	"net"
	"os"
	"time"
)

func Supported() bool                                { return false }
func owned(os.FileInfo) bool                         { return false }
func CheckDir(string, bool) error                    { return errors.New(UnsupportedHint) }
func checkSocket(string) error                       { return errors.New(UnsupportedHint) }
func verifyPeer(net.Conn) error                      { return errors.New(UnsupportedHint) }
func Dial(context.Context, string) (net.Conn, error) { return nil, errors.New(UnsupportedHint) }
func Listen(string) (net.Listener, error)            { return nil, errors.New(UnsupportedHint) }
func credentialRepair(string) string                 { return UnsupportedHint }
func InspectDirectory(string) (DirectoryStatus, error) {
	return DirectoryStatus{}, errors.New(UnsupportedHint)
}

func PrepareDirectory(string, time.Time) (*DirectoryTightening, error) {
	return nil, errors.New(UnsupportedHint)
}

func ConsumeTightening(string) (*DirectoryTightening, error) { return nil, errors.New(UnsupportedHint) }
func checkCredentialACL(string, os.FileInfo) error           { return errors.New(UnsupportedHint) }
