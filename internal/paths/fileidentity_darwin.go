//go:build darwin

package paths

import "syscall"

func ctimeOf(st *syscall.Stat_t) int64 {
	return st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec // int64 on every darwin target
}
