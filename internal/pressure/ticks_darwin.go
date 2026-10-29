//go:build darwin && cgo

package pressure

/*
#include <mach/mach_host.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

var host = C.mach_host_self()

func ticks() (busy, total uint64, err error) {
	var info C.host_cpu_load_info_data_t
	count := C.mach_msg_type_number_t(unsafe.Sizeof(info) / unsafe.Sizeof(C.integer_t(0)))
	if kr := C.host_statistics(host, C.HOST_CPU_LOAD_INFO, C.host_info_t(unsafe.Pointer(&info)), &count); kr != C.KERN_SUCCESS {
		return 0, 0, fmt.Errorf("host_statistics: kern_return %d", kr)
	}
	for _, t := range info.cpu_ticks {
		total += uint64(t)
	}
	return total - uint64(info.cpu_ticks[C.CPU_STATE_IDLE]), total, nil
}
