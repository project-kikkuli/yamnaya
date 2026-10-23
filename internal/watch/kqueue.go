//go:build darwin

package watch

import "syscall"

// kernel is a kqueue with one event-only descriptor per watched file or directory. A write, extension, attribute change, link,
// deletion or rename counts as a change and drops the watch, so the next look stats afresh and watches what is there now.
type kernel struct {
	kq     int
	fds    map[string]int
	paths  map[int]string
	gen    map[string]uint64
	events []syscall.Kevent_t
}

func newKernel() *kernel {
	kq, err := syscall.Kqueue()
	if err != nil {
		return nil
	}
	syscall.CloseOnExec(kq)
	return &kernel{kq: kq, fds: map[string]int{}, paths: map[int]string{}, gen: map[string]uint64{}, events: make([]syscall.Kevent_t, 256)}
}

const notes = syscall.NOTE_WRITE | syscall.NOTE_EXTEND | syscall.NOTE_ATTRIB | syscall.NOTE_DELETE | syscall.NOTE_RENAME | syscall.NOTE_REVOKE | syscall.NOTE_LINK

// drain takes every event the kernel queued, without waiting, and calls changed with each path it reports.
func (k *kernel) drain(changed func(string)) {
	zero := syscall.Timespec{}
	for {
		n, err := syscall.Kevent(k.kq, nil, k.events, &zero)
		if err != nil || n <= 0 {
			return
		}
		for _, e := range k.events[:n] {
			fd := int(e.Ident)
			path, ok := k.paths[fd]
			if !ok {
				continue
			}
			k.gen[path]++
			changed(path)
			delete(k.paths, fd)
			delete(k.fds, path)
			syscall.Close(fd)
		}
		if n < len(k.events) {
			return
		}
	}
}

// quiet reports whether path is watched and has not changed since its change count was gen.
func (k *kernel) quiet(path string, gen uint64) bool {
	_, ok := k.fds[path]
	return ok && k.gen[path] == gen
}

// watch makes sure path is watched, false when it cannot be.
func (k *kernel) watch(path string) bool {
	if _, ok := k.fds[path]; ok {
		return true
	}
	fd, err := syscall.Open(path, syscall.O_EVTONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	ev := syscall.Kevent_t{Ident: uint64(fd), Filter: syscall.EVFILT_VNODE, Flags: syscall.EV_ADD | syscall.EV_CLEAR | syscall.EV_ONESHOT, Fflags: notes}
	if _, err := syscall.Kevent(k.kq, []syscall.Kevent_t{ev}, nil, nil); err != nil {
		syscall.Close(fd)
		return false
	}
	k.fds[path], k.paths[fd] = fd, path
	return true
}
