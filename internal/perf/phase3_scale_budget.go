//go:build !race

package perf

import "time"

func phase3ScaleQueryBudget() time.Duration { return 2 * time.Second }
