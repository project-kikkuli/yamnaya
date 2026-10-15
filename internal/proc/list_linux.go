package proc

// List reads /proc directly, which costs a few milliseconds where ps costs tens.
func List() ([]Proc, error) { return readProc("/proc") }
