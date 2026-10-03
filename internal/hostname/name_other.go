//go:build !darwin

package hostname

import (
	"context"
	"os"
)

func platformName(context.Context) (string, error) { return os.Hostname() }
