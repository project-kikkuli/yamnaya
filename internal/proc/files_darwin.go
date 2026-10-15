//go:build darwin && cgo

package proc

/*
#include <libproc.h>
#include <sys/proc_info.h>
*/
import "C"

import "unsafe"

// Cwd is a process's working directory, or "".
func Cwd(pid int) string {
	var vi C.struct_proc_vnodepathinfo
	n := C.proc_pidinfo(C.int(pid), C.PROC_PIDVNODEPATHINFO, 0, unsafe.Pointer(&vi), C.int(unsafe.Sizeof(vi)))
	if n != C.int(unsafe.Sizeof(vi)) {
		return ""
	}
	return C.GoString(&vi.pvi_cdir.vip_path[0])
}

// Files are the paths a process holds open.
func Files(pid int) []string {
	size := C.proc_pidinfo(C.int(pid), C.PROC_PIDLISTFDS, 0, nil, 0)
	if size <= 0 {
		return nil
	}
	fds := make([]C.struct_proc_fdinfo, int(size)/int(unsafe.Sizeof(C.struct_proc_fdinfo{}))+16)
	size = C.proc_pidinfo(C.int(pid), C.PROC_PIDLISTFDS, 0, unsafe.Pointer(&fds[0]), C.int(len(fds))*C.int(unsafe.Sizeof(fds[0])))
	if size <= 0 {
		return nil
	}
	var names []string
	for _, fd := range fds[:int(size)/int(unsafe.Sizeof(fds[0]))] {
		if fd.proc_fdtype != C.PROX_FDTYPE_VNODE {
			continue
		}
		var vi C.struct_vnode_fdinfowithpath
		n := C.proc_pidfdinfo(C.int(pid), fd.proc_fd, C.PROC_PIDFDVNODEPATHINFO, unsafe.Pointer(&vi), C.int(unsafe.Sizeof(vi)))
		if n == C.int(unsafe.Sizeof(vi)) {
			if p := C.GoString(&vi.pvip.vip_path[0]); p != "" {
				names = append(names, p)
			}
		}
	}
	return names
}
