// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/folderaccess"
)

// SetFolderAccess is called after host identity is established and before Run.
// Starting a probe is nonblocking; no filesystem read or log is on the writer.
func (e *Engine) SetFolderAccess(m *folderaccess.Monitor) {
	e.folderAccess = m
	for _, a := range e.state.Agents {
		if !a.Gone() {
			e.noteAgentFolders(a.Agent)
		}
	}
}

func (e *Engine) noteAgentFolders(info *core.AgentInfo) {
	if e.folderAccess == nil || info == nil || e.remoteHostOf(&core.Agent{Agent: info}) != "" {
		return
	}
	e.folderAccess.Directory(info.CWD)
}

func (e *Engine) decorateFolderAccess(b core.Result) {
	if e.folderAccess == nil {
		return
	}
	rows := e.folderAccess.Snapshot()
	b["folder_access"] = rows
	b["folder_access_advice"] = folderaccess.Advice(rows)
}
