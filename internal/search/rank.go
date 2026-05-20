package search

import "time"

// roleWeights scale relevance so dialogue outranks tool noise.
var roleWeights = map[string]float64{
	"user":        1.0,
	"assistant":   1.0,
	"thinking":    0.6,
	"tool_use":    0.5,
	"tool_result": 0.4,
}

const (
	recencyWeight  = 0.5  // max bonus added for a brand-new message
	recencyHalfDay = 30.0 // age (days) at which the recency bonus halves
)

// adjustedScore turns SQLite bm25 (lower = better) into a higher-is-better score
// weighted by role and recency. For the LIKE path bm25 is 0, so role + recency
// fully determine ordering.
func adjustedScore(bm25 float64, role string, ts int64) float64 {
	relevance := -bm25 // bm25 is negative for good matches; flip so higher = better
	w, ok := roleWeights[role]
	if !ok {
		w = 0.7
	}
	return relevance*w + recencyBonus(ts)
}

func recencyBonus(ts int64) float64 {
	if ts <= 0 {
		return 0
	}
	ageDays := time.Since(time.Unix(ts, 0)).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	return recencyWeight / (1 + ageDays/recencyHalfDay)
}
