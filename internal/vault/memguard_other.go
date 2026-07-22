//go:build !linux

package vault

// detectAvailableMemory is a no-op off Linux: the cgroup OOM-kill risk the guard
// defends against is a Linux-container concern, and there is no portable
// per-process memory ceiling to read elsewhere. The guard becomes best-effort
// (does not block) on these platforms.
func detectAvailableMemory() (uint64, bool) { return 0, false }
