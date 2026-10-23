//go:build !linux

package proc

import "os/exec"

func listPS() ([]Proc, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,tty=,etime=,args=").Output()
	if err != nil {
		return nil, err
	}
	return Parse(string(out)), nil
}
