// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin

package hostname

import (
	"context"
	"os"
)

func platformName(context.Context) (string, error) { return os.Hostname() }
