package pressure

import (
	"encoding/binary"
	"testing"
)

func TestThreadIdentityReadsOnlyEnvironmentAfterEveryArgument(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 3)
	raw = append(raw, []byte("/bin/zsh\x00\x00\x00zsh\x00-c\x00echo CODEX_THREAD_ID=wrong\x00PATH=/bin\x00CODEX_THREAD_ID=worker\x00TOKEN=unrelated\x00\x00")...)
	if got := threadFromArgs(raw); got != "worker" {
		t.Fatal(got)
	}
	for _, bad := range [][]byte{nil, {1, 2, 3}, raw[:20]} {
		if got := threadFromArgs(bad); got != "" {
			t.Fatal("accepted truncated arguments", got)
		}
	}
}
