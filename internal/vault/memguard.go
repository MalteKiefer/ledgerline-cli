package vault

import (
	"fmt"
	"strconv"
	"strings"
)

// argonHeadroom is the memory (bytes) the process needs beyond Argon2id's own
// memory matrix: the Go runtime baseline, request buffers, and blob chunks. The
// guard requires the host/cgroup ceiling to hold memBytes + this, so a
// derivation is refused with a clear error instead of being OOM-killed mid-way
// on a memory-constrained host (§4a / §31).
const argonHeadroom = 128 * 1024 * 1024 // 128 MiB

// availableMemory reports the effective memory ceiling for this process in bytes
// and whether it could be determined. It is a var so tests can inject a value;
// the real implementation is platform-specific (Linux reads cgroup + meminfo,
// other platforms return ok=false and the guard is a no-op).
var availableMemory = detectAvailableMemory

// checkMemoryFor fails closed when the host/cgroup cannot hold an Argon2id
// derivation of memBytes plus headroom. When the ceiling cannot be determined it
// does not block (best-effort — a workstation with no cgroup limit is fine).
func checkMemoryFor(memBytes uint64) error {
	avail, ok := availableMemory()
	if !ok {
		return nil
	}
	need := memBytes + argonHeadroom
	if avail < need {
		return fmt.Errorf(
			"vault: host memory ceiling ~%d MiB is below the ~%d MiB this key derivation needs "+
				"(Argon2id mem=%d MiB + %d MiB headroom); refusing rather than risk an OOM kill mid-derivation",
			avail>>20, need>>20, memBytes>>20, argonHeadroom>>20)
	}
	return nil
}

// minCeiling combines candidate ceilings, ignoring zero (unknown/unlimited)
// values, and returns the smallest and whether any was present.
func minCeiling(candidates ...uint64) (uint64, bool) {
	var best uint64
	found := false
	for _, c := range candidates {
		if c == 0 {
			continue
		}
		if !found || c < best {
			best = c
			found = true
		}
	}
	return best, found
}

// parseCgroupMax parses a cgroup memory.max / memory.limit_in_bytes value. It
// returns 0 for "max", an empty string, a parse error, or a sentinel "unlimited"
// value (>= 1<<62, as cgroup v1 uses a huge number to mean no limit).
func parseCgroupMax(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "max" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	if v >= 1<<62 {
		return 0 // effectively unlimited
	}
	return v
}

// parseMemAvailableKB extracts MemAvailable (in bytes) from /proc/meminfo text,
// returning 0 when absent or unparseable.
func parseMemAvailableKB(meminfo string) uint64 {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
