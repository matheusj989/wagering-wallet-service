//go:build failpoints

package failpoint

import (
	"fmt"
	"os"
	"syscall"
)

const Enabled = true

func Hit(name string) {
	if name == "" || os.Getenv("FAILPOINT") != name {
		return
	}
	fmt.Fprintf(os.Stderr, "failpoint %s reached, killing the process\n", name)
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
}
