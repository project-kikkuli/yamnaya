package proc

import "bytes"

func configValue(b []byte, key string) string {
	prefix := []byte(key + "=")
	for _, part := range bytes.Split(b, []byte{0}) {
		if bytes.HasPrefix(part, prefix) {
			return string(part[len(prefix):])
		}
	}
	return ""
}
