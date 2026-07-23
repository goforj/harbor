//go:build darwin && cgo

package projectterminal

/*
#include <errno.h>
#include <libproc.h>
#include <stdint.h>

static int harbor_process_cwd(int pid, uint64_t *device, uint64_t *inode) {
	struct proc_vnodepathinfo info;
	int size = proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &info, sizeof(info));
	if (size != sizeof(info)) {
		return errno == 0 ? EIO : errno;
	}
	*device = (uint64_t)info.pvi_cdir.vip_vi.vi_stat.vst_dev;
	*inode = (uint64_t)info.pvi_cdir.vip_vi.vi_stat.vst_ino;
	return 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"syscall"
)

// verifyProcessDirectory proves the child entered the exact pinned checkout inode.
func verifyProcessDirectory(pid int, directory *os.File) error {
	expected, err := directory.Stat()
	if err != nil {
		return fmt.Errorf("inspect pinned project directory: %w", err)
	}
	stat, ok := expected.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect pinned project directory identity")
	}

	var device C.uint64_t
	var inode C.uint64_t
	if code := C.harbor_process_cwd(C.int(pid), &device, &inode); code != 0 {
		return fmt.Errorf("inspect login shell working directory: %w", syscall.Errno(code))
	}
	if uint64(stat.Dev) != uint64(device) || uint64(stat.Ino) != uint64(inode) {
		return fmt.Errorf("login shell working directory differs from the pinned project directory")
	}
	return nil
}
