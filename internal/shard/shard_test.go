package shard

import "testing"

func TestRecommendedBits(t *testing.T) {
	// Anchored to the web store-v3-conformance test.
	cases := []struct {
		count int
		want  int
	}{
		{0, 0}, {100, 0}, {500, 0}, {501, 1}, {600, 1}, {1000, 1},
		{1001, 2}, {18000, 6},
	}
	for _, tc := range cases {
		if got := RecommendedBits(tc.count); got != tc.want {
			t.Fatalf("RecommendedBits(%d) = %d want %d", tc.count, got, tc.want)
		}
	}
}

func TestBucketOfSingleShard(t *testing.T) {
	if got := BucketOf("ffffffffffffffffffffffffffffffff", 0); got != 0 {
		t.Fatalf("bits=0 must map to bucket 0, got %d", got)
	}
}

func TestBucketOfTopBits(t *testing.T) {
	// prefix 0x80000000 → top bit set. At bits=1, that is bucket 1; a prefix with
	// the top bit clear is bucket 0.
	if got := BucketOf("80000000deadbeef", 1); got != 1 {
		t.Fatalf("BucketOf(0x80000000,1) = %d want 1", got)
	}
	if got := BucketOf("7fffffffdeadbeef", 1); got != 0 {
		t.Fatalf("BucketOf(0x7fffffff,1) = %d want 0", got)
	}
	// At bits=4, take the top nibble: 0xa... → 0xa = 10.
	if got := BucketOf("a1b2c3d4ffff0000", 4); got != 0xa {
		t.Fatalf("BucketOf top-nibble = %d want 10", got)
	}
}

func TestBucketOfDeterministicRange(t *testing.T) {
	bits := 6
	n := Count(bits)
	if n != 64 {
		t.Fatalf("Count(6) = %d want 64", n)
	}
	for _, id := range []string{
		"00000000aaaa", "ffffffffbbbb", "12345678cccc", "deadbeef0000",
	} {
		b := BucketOf(id, bits)
		if b < 0 || b >= n {
			t.Fatalf("bucket %d out of range [0,%d) for %s", b, n, id)
		}
		// Determinism.
		if BucketOf(id, bits) != b {
			t.Fatalf("non-deterministic bucket for %s", id)
		}
	}
}
