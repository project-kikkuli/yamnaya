package pressure

// Host is the machine's memory and processor state at one reading. Level is the kernel's memory pressure: 1 normal,
// 2 warn, 4 critical, 0 when the kernel reports none. Busy is the share of processor time spent busy since the
// previous reading; waiting on disk is not busy.
type Host struct {
	Memory, Free, Swap uint64
	Level              int
	Busy               float64
}

// Meter reads the host, keeping the previous processor ticks so Busy covers the time between readings.
type Meter struct{ busy, total uint64 }

func (m *Meter) Read() (Host, error) {
	h, err := memory()
	if err != nil {
		return h, err
	}
	busy, total, err := ticks()
	if err != nil {
		return h, err
	}
	if m.total > 0 && total > m.total {
		h.Busy = float64(busy-m.busy) / float64(total-m.total)
	}
	m.busy, m.total = busy, total
	return h, nil
}
