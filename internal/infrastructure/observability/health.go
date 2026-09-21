package observability

import (
	"context"
	"sync"
	"time"
)

const checkTimeout = 2 * time.Second

type Probe func(ctx context.Context) error

type CheckResult struct {
	Name   string
	Up     bool
	Reason string
}

type Health struct {
	probes map[string]Probe
}

func NewHealth() *Health {
	return &Health{probes: make(map[string]Probe)}
}

func (h *Health) Register(name string, probe Probe) {
	h.probes[name] = probe
}

func (h *Health) Ready(ctx context.Context) (bool, []CheckResult) {
	results := make([]CheckResult, len(h.probes))
	names := make([]string, 0, len(h.probes))
	for name := range h.probes {
		names = append(names, name)
	}

	var waiting sync.WaitGroup
	for index, name := range names {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			probeCtx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()

			result := CheckResult{Name: name, Up: true}
			if err := h.probes[name](probeCtx); err != nil {
				result.Up = false
				result.Reason = err.Error()
			}
			results[index] = result
		}()
	}
	waiting.Wait()

	ready := true
	for _, result := range results {
		if !result.Up {
			ready = false
		}
	}
	return ready, results
}
