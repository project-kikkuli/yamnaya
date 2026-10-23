//go:build !darwin

package watch

type kernel struct{ gen map[string]uint64 }

func newKernel() *kernel                    { return nil }
func (k *kernel) drain(func(string))        {}
func (k *kernel) quiet(string, uint64) bool { return false }
func (k *kernel) watch(string) bool         { return false }
