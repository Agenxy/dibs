package main

import (
	"errors"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/paths"
)

// socketWakesOn is [wake] sockets as this board's dibs.toml has it: the
// bridge's self-wake is one of the session-socket routes that setting
// switches off. A file with a setting this build does not know still answers.
func socketWakesOn() bool {
	return socketWakesOnIn(paths.DataDir())
}

func socketWakesOnIn(dir string) bool {
	cfg, err := readBoardConfig(dir)
	var unknown *boardconfig.UnknownSettingsError
	if err != nil && !errors.As(err, &unknown) {
		return true // an unreadable file switches nothing off; the daemon refuses it
	}
	return cfg.Wake.Sockets == nil || *cfg.Wake.Sockets
}
