package vault

import "testing"

func TestValidateKDFBounds(t *testing.T) {
	cases := []struct {
		name    string
		ops     uint64
		mem     uint64
		wantErr bool
	}{
		{"contract params", 4, 256 * 1024 * 1024, false},
		{"min ok", minKdfOps, minKdfMem, false},
		{"max ok", maxKdfOps, maxKdfMem, false},
		{"ops zero", 0, 256 * 1024 * 1024, true},
		{"ops too high", maxKdfOps + 1, 256 * 1024 * 1024, true},
		{"mem too small", 4, minKdfMem - 1, true},
		{"mem too large", 4, maxKdfMem + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateKDF(tc.ops, tc.mem)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateKDF(%d,%d) err=%v wantErr=%v", tc.ops, tc.mem, err, tc.wantErr)
			}
		})
	}
}

func TestParseCgroupMax(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"max", 0},
		{"", 0},
		{"  \n", 0},
		{"134217728", 128 * 1024 * 1024},
		{"134217728\n", 128 * 1024 * 1024},
		{"9223372036854771712", 0}, // cgroup v1 "unlimited" sentinel
		{"not-a-number", 0},
	}
	for _, tc := range cases {
		if got := parseCgroupMax(tc.in); got != tc.want {
			t.Fatalf("parseCgroupMax(%q) = %d want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseMemAvailableKB(t *testing.T) {
	meminfo := "MemTotal:       16384000 kB\nMemFree:         1000000 kB\nMemAvailable:    2097152 kB\nBuffers:          100000 kB\n"
	if got := parseMemAvailableKB(meminfo); got != 2097152*1024 {
		t.Fatalf("parseMemAvailableKB = %d want %d", got, 2097152*1024)
	}
	if got := parseMemAvailableKB("MemTotal: 100 kB\n"); got != 0 {
		t.Fatalf("absent MemAvailable should be 0, got %d", got)
	}
}

func TestMinCeiling(t *testing.T) {
	if _, ok := minCeiling(0, 0); ok {
		t.Fatal("all-zero must be not-found")
	}
	if v, ok := minCeiling(100, 0); !ok || v != 100 {
		t.Fatalf("got %d,%v want 100,true", v, ok)
	}
	if v, ok := minCeiling(100, 50, 0, 75); !ok || v != 50 {
		t.Fatalf("got %d,%v want 50,true", v, ok)
	}
}

func TestCheckMemoryFor(t *testing.T) {
	orig := availableMemory
	t.Cleanup(func() { availableMemory = orig })

	const mem = 256 * 1024 * 1024

	// Ceiling below mem+headroom → fail closed.
	availableMemory = func() (uint64, bool) { return 300 * 1024 * 1024, true }
	if err := checkMemoryFor(mem); err == nil {
		t.Fatal("expected fail-closed when host memory below need")
	}

	// Ample ceiling → allowed.
	availableMemory = func() (uint64, bool) { return 2 * 1024 * 1024 * 1024, true }
	if err := checkMemoryFor(mem); err != nil {
		t.Fatalf("ample memory should pass: %v", err)
	}

	// Undeterminable ceiling → best-effort, does not block.
	availableMemory = func() (uint64, bool) { return 0, false }
	if err := checkMemoryFor(mem); err != nil {
		t.Fatalf("undeterminable ceiling must not block: %v", err)
	}
}
