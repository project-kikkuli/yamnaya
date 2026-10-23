package slot

import (
	"regexp"
	"strconv"
	"strings"
)

// Workers leaves two cores for the host and divides the remainder between slots.
func (p Pool) Workers() int { return max(1, (p.Cores-2)/max(1, p.Capacity)) }

var goParallel = regexp.MustCompile(`(?:^|\s)["']?-p=([0-9]+)["']?`)
var makeParallel = regexp.MustCompile(`(?:^|\s)(?:-j\s*|--jobs[= ]?)([0-9]+)`)
var jobserver = regexp.MustCompile(`(?:^|\s)--jobserver-(?:auth|fds)=\S+`)
var nodeParallel = regexp.MustCompile(`(?:^|\s)--v8-pool-size[= ]([0-9]+)`)

// Environment bounds common worker pools, preserving smaller inherited limits.
func Environment(env []string, workers int) []string {
	workers = max(1, workers)
	values := make(map[string]string, len(env))
	var keys []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if _, exists := values[key]; !exists {
				keys = append(keys, key)
			}
			values[key] = value
		}
	}
	set := func(key, value string) {
		if _, exists := values[key]; !exists {
			keys = append(keys, key)
		}
		values[key] = value
	}
	for _, key := range []string{
		"GOMAXPROCS", "CARGO_BUILD_JOBS", "RAYON_NUM_THREADS", "CMAKE_BUILD_PARALLEL_LEVEL",
		"UV_THREADPOOL_SIZE", "npm_config_jobs", "PYTEST_XDIST_AUTO_NUM_WORKERS",
		"OMP_NUM_THREADS", "OPENBLAS_NUM_THREADS", "MKL_NUM_THREADS", "NUMEXPR_NUM_THREADS",
		"POLARS_MAX_THREADS", "UV_CONCURRENT_BUILDS", "MAX_JOBS",
	} {
		n := workers
		if old, err := strconv.Atoi(values[key]); err == nil && old > 0 {
			n = min(n, old)
		}
		set(key, strconv.Itoa(n))
	}
	for _, flag := range []struct {
		key, suffix string
		pattern     *regexp.Regexp
	}{
		{"GOFLAGS", "-p=", goParallel},
		{"MAKEFLAGS", "-j", makeParallel},
		{"NODE_OPTIONS", "--v8-pool-size=", nodeParallel},
	} {
		n, value := workers, values[flag.key]
		for _, match := range flag.pattern.FindAllStringSubmatch(value, -1) {
			if old, err := strconv.Atoi(match[1]); err == nil && old > 0 {
				n = min(n, old)
			}
		}
		if flag.key == "MAKEFLAGS" {
			value = jobserver.ReplaceAllString(value, "")
		}
		set(flag.key, strings.TrimSpace(value+" "+flag.suffix+strconv.Itoa(n)))
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}
