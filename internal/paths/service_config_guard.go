// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

// CatchServiceConfig turns only the legacy string accessors' typed selection
// failure into a command error. Unrelated programming panics still propagate.
func CatchServiceConfig(run func()) (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			if config, ok := failure.(*ServiceConfigError); ok {
				err = config
				return
			}
			panic(failure)
		}
	}()
	run()
	return nil
}
