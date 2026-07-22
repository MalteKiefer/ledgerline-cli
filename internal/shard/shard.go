// Package shard implements Store v3 content-addressed, id-bucketed sharding
// (spec §5.1), byte-compatible with the web client (resources/js/shared/shard.js).
//
// A record's shard bucket is derived purely from its id, so every client agrees
// on the bucket regardless of load/insert order — no array-position cascade, no
// cross-client thrash. Any edit touches exactly one bucket.
//
//	shardCount = 2^shardBits
//	bucket(id) = uint32(hexPrefix8(id)) >> (32 - shardBits)   // top shardBits bits
package shard

import "strconv"

// Count returns the number of shards for a given shardBits.
func Count(shardBits int) int { return 1 << uint(shardBits) }

// BucketOf returns the shard bucket index for a record id at a given shardBits,
// in [0, 2^shardBits). shardBits <= 0 is a single shard (bucket 0). An id with
// fewer than 8 hex chars, or a non-hex prefix, maps to bucket 0.
func BucketOf(id string, shardBits int) int {
	if shardBits <= 0 {
		return 0
	}
	if len(id) < 8 {
		return 0
	}
	prefix, err := strconv.ParseUint(id[:8], 16, 64)
	if err != nil {
		return 0
	}
	return int(uint32(prefix) >> uint(32-shardBits))
}

// RecommendedBits returns the shardBits for a record count: keep the mean at
// ≈250 records/shard, split (bits += 1) when the mean would exceed ~500, starting
// at 0 for small libraries (§5.1). Matches shard.js recommendedShardBits:
// while count/2^bits > 500 → bits++.
func RecommendedBits(count int) int {
	bits := 0
	for count > 500*(1<<uint(bits)) {
		bits++
	}
	return bits
}
