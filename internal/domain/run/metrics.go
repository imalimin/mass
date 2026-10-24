package run

// CacheStats includes coverage explicitly. Unknown usage is never a zero hit.
type CacheStats struct {
	Attempts      int      `json:"attempts"`
	WithCacheRead int      `json:"with_cache_read"`
	CacheHits     int      `json:"cache_hits"`
	PairedUsage   int      `json:"paired_usage"`
	HitRate       *float64 `json:"hit_rate"`
	InputRatio    *float64 `json:"cache_input_ratio"`
	Coverage      *float64 `json:"coverage"`
}

func MeasureCache(usages []Usage) CacheStats {
	result := CacheStats{Attempts: len(usages)}
	var input, cache int64
	for _, u := range usages {
		if u.CacheRead != nil {
			result.WithCacheRead++
			if *u.CacheRead > 0 {
				result.CacheHits++
			}
		}
		if u.CacheRead != nil && u.Input != nil && *u.CacheRead <= *u.Input {
			result.PairedUsage++
			input += *u.Input
			cache += *u.CacheRead
		}
	}
	ratio := func(a, b int) *float64 {
		if b == 0 {
			return nil
		}
		v := float64(a) / float64(b)
		return &v
	}
	result.Coverage = ratio(result.WithCacheRead, result.Attempts)
	result.HitRate = ratio(result.CacheHits, result.WithCacheRead)
	if input > 0 {
		v := float64(cache) / float64(input)
		result.InputRatio = &v
	}
	return result
}
