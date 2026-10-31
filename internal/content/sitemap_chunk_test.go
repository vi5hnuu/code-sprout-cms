package content

import "testing"

// Content ids are shard-prefixed (migration 00011_id_ranges.sql): id = (shard << 47) | seq.
// On any shard > 0 the global quotient id/50_000 overflows int4, so sitemap_chunk.chunk and
// every ChunkRange return value is int64 end-to-end. These tests pin the arithmetic so a
// future refactor cannot silently narrow a type and reintroduce the overflow.
const idShardBits = 47

func TestChunkRange_Shard0(t *testing.T) {
	for _, c := range []struct {
		name       string
		id         int64
		wantChunk  int64
		wantLo, wantHi int64
	}{
		{"zero id", 0, 0, 0, 50_000},
		{"first non-zero", 1, 0, 0, 50_000},
		{"edge of chunk 0", 49_999, 0, 0, 50_000},
		{"first of chunk 1", 50_000, 1, 50_000, 100_000},
		{"deep into chunk 2 000 000", 100_000_000_000, 2_000_000, 100_000_000_000, 100_000_050_000},
	} {
		t.Run(c.name, func(t *testing.T) {
			chunk, lo, hi := ChunkRange(c.id)
			if chunk != c.wantChunk || lo != c.wantLo || hi != c.wantHi {
				t.Fatalf("ChunkRange(%d) = (%d, %d, %d); want (%d, %d, %d)",
					c.id, chunk, lo, hi, c.wantChunk, c.wantLo, c.wantHi)
			}
		})
	}
}

// The whole point: on shard 1+ the chunk number is well above int4's 2.1-billion ceiling. If a
// future change narrows a column or a struct field back to int32, this test fails.
func TestChunkRange_ShardsAbove0(t *testing.T) {
	for shard := int64(1); shard <= 3; shard++ {
		shardLo := shard << idShardBits
		id := shardLo + 1 // first id on the shard
		chunk, lo, hi := ChunkRange(id)
		wantChunk := shardLo / SitemapChunkSize
		if chunk != wantChunk {
			t.Fatalf("shard %d: chunk = %d; want %d", shard, chunk, wantChunk)
		}
		if chunk <= int64(^uint32(0)>>1) {
			t.Fatalf("shard %d: chunk %d still fits in int32 — test needs updating or shard math broke", shard, chunk)
		}
		if lo != wantChunk*SitemapChunkSize || hi != (wantChunk+1)*SitemapChunkSize {
			t.Fatalf("shard %d: (lo,hi) = (%d,%d); want (%d,%d)",
				shard, lo, hi, wantChunk*SitemapChunkSize, (wantChunk+1)*SitemapChunkSize)
		}
		// Any two ids within the same 50 000-wide window must share a chunk.
		if alt, _, _ := ChunkRange(id + SitemapChunkSize - 1); alt != chunk {
			t.Fatalf("shard %d: id %d and %d gave different chunks (%d vs %d)",
				shard, id, id+SitemapChunkSize-1, chunk, alt)
		}
		// The next id crosses into the next chunk.
		if next, _, _ := ChunkRange(id + SitemapChunkSize); next != chunk+1 {
			t.Fatalf("shard %d: id %d expected chunk %d, got %d",
				shard, id+SitemapChunkSize, chunk+1, next)
		}
	}
}
