//go:build darwin && (amd64 || arm64)

#include "textflag.h"

TEXT libc_runtime_repair_proc_pidpath_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_runtime_repair_proc_pidpath(SB)
GLOBL	·darwinRuntimeRepairPIDPathTrampolineAddress(SB), RODATA, $8
DATA	·darwinRuntimeRepairPIDPathTrampolineAddress(SB)/8, $libc_runtime_repair_proc_pidpath_trampoline<>(SB)

TEXT libc_runtime_repair_proc_pidinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_runtime_repair_proc_pidinfo(SB)
GLOBL	·darwinRuntimeRepairPIDInfoTrampolineAddress(SB), RODATA, $8
DATA	·darwinRuntimeRepairPIDInfoTrampolineAddress(SB)/8, $libc_runtime_repair_proc_pidinfo_trampoline<>(SB)

TEXT libc_runtime_repair_proc_pidfdinfo_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_runtime_repair_proc_pidfdinfo(SB)
GLOBL	·darwinRuntimeRepairPIDFDInfoTrampolineAddress(SB), RODATA, $8
DATA	·darwinRuntimeRepairPIDFDInfoTrampolineAddress(SB)/8, $libc_runtime_repair_proc_pidfdinfo_trampoline<>(SB)
