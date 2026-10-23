package proc

import (
	"os"
	"strconv"
)

func ConfigRoot(pid int, key string) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	return configValue(b, key)
}
