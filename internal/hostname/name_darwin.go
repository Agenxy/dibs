package hostname

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

var errUnset = errors.New("name is not set")

func platformName(ctx context.Context) (string, error) { return macName(ctx, scutilName) }

func macName(ctx context.Context, get func(context.Context, string) (string, error)) (string, error) {
	for _, key := range []string{"HostName", "LocalHostName", "ComputerName"} {
		value, err := get(ctx, key)
		if errors.Is(err, errUnset) {
			continue
		}
		if err != nil {
			return "", err
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, nil
		}
	}
	return "", errUnset
}

func scutilName(ctx context.Context, key string) (string, error) {
	// Fixed executable and argv, no shell, and all three reads share one deadline.
	var cmd *exec.Cmd
	switch key {
	case "HostName":
		cmd = exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "HostName")
	case "LocalHostName":
		cmd = exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "LocalHostName")
	case "ComputerName":
		cmd = exec.CommandContext(ctx, "/usr/sbin/scutil", "--get", "ComputerName")
	default:
		return "", errors.New("unsupported computer name key")
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && strings.Contains(string(out), "not set") {
		return "", errUnset
	}
	return "", err
}
