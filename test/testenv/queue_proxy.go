//go:build integration

package testenv

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
)

// QueueProxy injects only DLQ SendMessage failures; every other action still
// reaches the real broker. A timeout can happen after the broker persisted the
// message, reproducing an ambiguous publication without mocking SQS storage.
type QueueProxy struct {
	server   *httptest.Server
	failing  atomic.Bool
	attempts atomic.Int64
}

func (p *QueueProxy) URL() string     { return p.server.URL }
func (p *QueueProxy) Restore()        { p.failing.Store(false) }
func (p *QueueProxy) Attempts() int64 { return p.attempts.Load() }
func (e *Env) StartQueueProxy(mode string) *QueueProxy {
	e.t.Helper()
	target, err := url.Parse(e.suite.sqsEndpoint())
	if err != nil {
		e.t.Fatal(err)
	}
	proxy := &QueueProxy{}
	proxy.failing.Store(true)
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request unreadable", 400)
			return
		}
		var input struct {
			QueueURL string `json:"QueueUrl"`
		}
		_ = json.Unmarshal(raw, &input)
		inject := proxy.failing.Load() && input.QueueURL == e.Queues.DLQ && strings.HasSuffix(r.Header.Get("X-Amz-Target"), "SendMessage")
		if inject {
			proxy.attempts.Add(1)
			if mode == "unavailable" {
				http.Error(w, "temporarily unavailable", 503)
				return
			}
		}
		forward := r.Clone(r.Context())
		forward.URL.Scheme = target.Scheme
		forward.URL.Host = target.Host
		forward.Host = target.Host
		forward.RequestURI = ""
		forward.Body = io.NopCloser(bytes.NewReader(raw))
		response, err := http.DefaultTransport.RoundTrip(forward)
		if err != nil {
			http.Error(w, "upstream unavailable", 502)
			return
		}
		defer response.Body.Close()
		if inject && mode == "timeout" {
			_, _ = io.Copy(io.Discard, response.Body)
			<-r.Context().Done()
			return
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	e.t.Cleanup(proxy.server.Close)
	return proxy
}
