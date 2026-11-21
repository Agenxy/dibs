// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"encoding/binary"
	"math"
)

// OwnsStdioPipes checks the input and output pipe pairs against the named
// parent. A child spawned by a shell or a terminal is insufficient evidence.
func OwnsStdioPipes(child, parent int) bool {
	input, output := readPipePair(child, 0), readPipePair(child, 1)
	if input == (pipePair{}) || output == (pipePair{}) || input == output {
		return false
	}
	fds, err := descriptorInventory(parent)
	if err != nil {
		return false
	}
	var hasInput, hasOutput bool
	for off := 0; off < len(fds); off += 8 {
		fd := binary.LittleEndian.Uint32(fds[off : off+4])
		kind := binary.LittleEndian.Uint32(fds[off+4 : off+8])
		if fd > math.MaxInt32 || kind != pipeFDType {
			continue
		}
		pair := readPipePair(parent, int(fd))
		hasInput = hasInput || pair == input.reversed()
		hasOutput = hasOutput || pair == output.reversed()
		if hasInput && hasOutput {
			return true
		}
	}
	return false
}

const (
	pipeFDType   = 6   // PROX_FDTYPE_PIPE
	pipeFDInfo   = 6   // PROC_PIDFDPIPEINFO
	pipeInfoSize = 184 // struct pipe_fdinfo, checked on the Darwin SDK
	pipeHandle   = 160
)

type pipePair struct{ local, peer uint64 }

func (p pipePair) reversed() pipePair { return pipePair{p.peer, p.local} }

func readPipePair(pid, fd int) pipePair {
	var b [pipeInfoSize]byte
	n, err := readInfo(pidFDCall, pid, pipeFDInfo, fd, b[:])
	if err != nil || n != len(b) {
		return pipePair{}
	}
	local := binary.LittleEndian.Uint64(b[pipeHandle:])
	peer := binary.LittleEndian.Uint64(b[pipeHandle+8:])
	if local == 0 || peer == 0 || local == peer {
		return pipePair{}
	}
	return pipePair{local, peer}
}
