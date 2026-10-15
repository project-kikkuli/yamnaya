//go:build !darwin && !linux

package proc

// List runs ps once and returns every process.
func List() ([]Proc, error) { return listPS() }
