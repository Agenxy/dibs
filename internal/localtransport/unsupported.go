// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package localtransport

import (
	"context"
	"errors"
	"net"
	"os"
)

func Supported() bool                                { return false }
func owned(os.FileInfo) bool                         { return false }
func CheckDir(string, bool) error                    { return errors.New(UnsupportedHint) }
func checkSocket(string) error                       { return errors.New(UnsupportedHint) }
func verifyPeer(net.Conn) error                      { return errors.New(UnsupportedHint) }
func Dial(context.Context, string) (net.Conn, error) { return nil, errors.New(UnsupportedHint) }
func Listen(string) (net.Listener, error)            { return nil, errors.New(UnsupportedHint) }
