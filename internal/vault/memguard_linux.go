//go:build linux

package vault

import "os"

// detectAvailableMemory reads the effective memory ceiling on Linux: the smaller
// of the cgroup memory limit (v2 memory.max, then v1 memory.limit_in_bytes) and
// /proc/meminfo MemAvailable. Returns ok=false when nothing is determinable.
func detectAvailableMemory() (uint64, bool) {
	var cgroup uint64
	if b, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil { // cgroup v2
		cgroup = parseCgroupMax(string(b))
	}
	if cgroup == 0 {
		if b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil { // cgroup v1
			cgroup = parseCgroupMax(string(b))
		}
	}

	var memAvail uint64
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		memAvail = parseMemAvailableKB(string(b))
	}

	return minCeiling(cgroup, memAvail)
}
