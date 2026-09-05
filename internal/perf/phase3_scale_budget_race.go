//go:build race

package perf

import "time"

// The race instrumenter substantially increases SQLite scan and allocation
// cost. Keep the production budget in phase3_scale_budget.go; this wider
// diagnostic-only ceiling prevents instrumentation overhead from hiding race
// reports behind a false performance failure.
func phase3ScaleQueryBudget() time.Duration { return 5 * time.Second }
