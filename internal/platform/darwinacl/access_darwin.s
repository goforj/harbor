//go:build darwin && (amd64 || arm64)

#include "textflag.h"

TEXT libc_filesec_init_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_filesec_init(SB)
GLOBL	·filesecInitTrampolineAddress(SB), RODATA, $8
DATA	·filesecInitTrampolineAddress(SB)/8, $libc_filesec_init_trampoline<>(SB)

TEXT libc_filesec_free_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_filesec_free(SB)
GLOBL	·filesecFreeTrampolineAddress(SB), RODATA, $8
DATA	·filesecFreeTrampolineAddress(SB)/8, $libc_filesec_free_trampoline<>(SB)

TEXT libc_filesec_set_property_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_filesec_set_property(SB)
GLOBL	·filesecSetPropertyTrampolineAddress(SB), RODATA, $8
DATA	·filesecSetPropertyTrampolineAddress(SB)/8, $libc_filesec_set_property_trampoline<>(SB)

TEXT libc_fchmodx_np_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_fchmodx_np(SB)
GLOBL	·fchmodxTrampolineAddress(SB), RODATA, $8
DATA	·fchmodxTrampolineAddress(SB)/8, $libc_fchmodx_np_trampoline<>(SB)
