//go:build integration

package testenv

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Metrics is a snapshot of the Prometheus endpoint, so a scenario can assert that
// an outcome was counted without parsing the exposition format by hand.
type Metrics struct {
	raw string
}

func (a *App) Metrics() Metrics {
	a.t.Helper()

	response, err := a.Client.Get(a.BaseURL + "/metrics")
	if err != nil {
		a.t.Fatalf("metrics could not be read: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		a.t.Fatalf("metrics could not be read: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		a.t.Fatalf("metrics answered %d", response.StatusCode)
	}
	return Metrics{raw: string(body)}
}

// Value sums every series of the metric whose labels contain all the given
// name=value pairs.
func (m Metrics) Value(name string, labels ...string) float64 {
	total := 0.0
	for _, line := range strings.Split(m.raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		seriesName, labelSet, _ := strings.Cut(series, "{")
		if seriesName != name {
			continue
		}
		if !matchesLabels(labelSet, labels) {
			continue
		}
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			total += parsed
		}
	}
	return total
}

func (m Metrics) Has(name string, atLeast float64, labels ...string) bool {
	return m.Value(name, labels...) >= atLeast
}

func (m Metrics) Lines(prefix string) string {
	var found []string
	for _, line := range strings.Split(m.raw, "\n") {
		if strings.HasPrefix(line, prefix) {
			found = append(found, line)
		}
	}
	if len(found) == 0 {
		return "no series named " + prefix
	}
	return strings.Join(found, "\n")
}

func matchesLabels(labelSet string, wanted []string) bool {
	for _, label := range wanted {
		name, value, found := strings.Cut(label, "=")
		if !found {
			continue
		}
		if !strings.Contains(labelSet, name+"=\""+value+"\"") {
			return false
		}
	}
	return true
}
