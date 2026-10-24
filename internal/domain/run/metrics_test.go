package run

import "testing"

func TestCacheCoverage(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	empty := MeasureCache(nil)
	if empty.HitRate != nil || empty.InputRatio != nil || empty.Coverage != nil {
		t.Fatal("empty stats fabricated")
	}
	stats := MeasureCache([]Usage{{}, {Input: n(100), CacheRead: n(0)}, {Input: n(100), CacheRead: n(50)}, {CacheRead: n(20)}})
	if stats.Attempts != 4 || stats.WithCacheRead != 3 || stats.PairedUsage != 2 || *stats.HitRate != 2.0/3 || *stats.InputRatio != 0.25 || *stats.Coverage != 0.75 {
		t.Fatal(stats)
	}
}
