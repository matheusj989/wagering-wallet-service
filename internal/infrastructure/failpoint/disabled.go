//go:build !failpoints

package failpoint

const Enabled = false

func Hit(string) {}
