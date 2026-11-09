// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

#include "textflag.h"

TEXT pidInfoTrampoline<>(SB),NOSPLIT,$0-0
	JMP dibs_proc_pidinfo(SB)
GLOBL ·pidInfoAddr(SB), RODATA, $8
DATA ·pidInfoAddr(SB)/8, $pidInfoTrampoline<>(SB)

TEXT pidFDInfoTrampoline<>(SB),NOSPLIT,$0-0
	JMP dibs_proc_pidfdinfo(SB)
GLOBL ·pidFDInfoAddr(SB), RODATA, $8
DATA ·pidFDInfoAddr(SB)/8, $pidFDInfoTrampoline<>(SB)

TEXT pidPathTrampoline<>(SB),NOSPLIT,$0-0
	JMP dibs_proc_pidpath(SB)
GLOBL ·pidPathAddr(SB), RODATA, $8
DATA ·pidPathAddr(SB)/8, $pidPathTrampoline<>(SB)
