//go:build integration

package testenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	raceExitCode  = 66
	readyDeadline = 45 * time.Second
)

type AppOption func(map[string]string)

func WithFailpoint(name string) AppOption {
	return func(environment map[string]string) { environment["FAILPOINT"] = name }
}

func WithoutConsumer() AppOption {
	return func(environment map[string]string) { environment["SQS_CONSUMER_ENABLED"] = "false" }
}

func WithoutOutboxPublisher() AppOption {
	return func(environment map[string]string) { environment["OUTBOX_PUBLISHER_ENABLED"] = "false" }
}

func WithoutReferenceWorker() AppOption {
	return func(environment map[string]string) { environment["REFERENCE_WORKER_ENABLED"] = "false" }
}

func WithSetting(name string, value string) AppOption {
	return func(environment map[string]string) { environment[name] = value }
}

// App is one real service process: its own pool, its own memory and the race
// detector active, supervised until the test ends.
type App struct {
	t       *testing.T
	command *exec.Cmd
	BaseURL string
	Client  *http.Client

	output         *syncBuffer
	exited         chan struct{}
	outputRead     chan struct{}
	mutex          sync.Mutex
	killExpected   bool
	stopExpected   bool
	failpointArmed bool
	finished       bool
}

func (e *Env) StartApp(instances int, options ...AppOption) []*App {
	e.t.Helper()

	started := make([]*App, 0, instances)
	for index := range instances {
		started = append(started, e.startOne(index, options...))
	}
	return started
}

func (e *Env) baseEnvironment(index int) map[string]string {
	return map[string]string{
		"APP_ENV":                 "test",
		"INSTANCE_ID":             fmt.Sprintf("%s-%d", e.database, index),
		"LOG_LEVEL":               "info",
		"HTTP_ADDR":               "127.0.0.1:0",
		"DATABASE_URL":            e.DB.URL(),
		"DB_POOL_MAX_CONNS":       "20",
		"DB_POOL_MIN_CONNS":       "0",
		"AWS_REGION":              "us-east-1",
		"AWS_ACCESS_KEY_ID":       "test",
		"AWS_SECRET_ACCESS_KEY":   "test",
		"SQS_ENDPOINT":            e.suite.sqsEndpoint(),
		"SQS_WAGER_QUEUE_URL":     e.Queues.Wager,
		"SQS_WAGER_DLQ_URL":       e.Queues.DLQ,
		"SQS_EVENTS_QUEUE_URL":    e.Queues.Events,
		"SQS_WAIT_TIME":           "1s",
		"SQS_VISIBILITY_TIMEOUT":  "5s",
		"SQS_MESSAGE_DEADLINE":    "2s",
		"SQS_RETRY_BACKOFF_BASE":  "1s",
		"SQS_MAX_RECEIVE_COUNT":   testMaxReceiveCount,
		"OUTBOX_POLL_INTERVAL":    "200ms",
		"OUTBOX_LEASE":            "3s",
		"REFERENCE_POLL_INTERVAL": "200ms",
		"REFERENCE_BACKOFF_BASE":  "200ms",
		"REFERENCE_BACKOFF_MAX":   "1s",
		"REFERENCE_TTL":           "5s",
		"OIDC_ISSUER":             publicIssuer,
		"OIDC_DISCOVERY_URL":      e.Auth.DiscoveryURL(),
		"OIDC_JWKS_URL":           e.Auth.JWKSURL(),
		"OIDC_AUDIENCE":           "wallet-api",
		"GORACE":                  fmt.Sprintf("halt_on_error=1 exitcode=%d", raceExitCode),
	}
}

func (e *Env) startOne(index int, options ...AppOption) *App {
	e.t.Helper()

	environment := e.baseEnvironment(index)
	for _, option := range options {
		option(environment)
	}

	command := exec.Command(e.suite.binary)
	command.Env = os.Environ()
	for name, value := range environment {
		command.Env = append(command.Env, name+"="+value)
	}

	output := &syncBuffer{}
	pipe, writer, err := os.Pipe()
	if err != nil {
		e.t.Fatalf("service output could not be captured: %v", err)
	}
	command.Stderr = output
	command.Stdout = writer

	instance := &App{
		t:              e.t,
		command:        command,
		Client:         &http.Client{Timeout: 20 * time.Second},
		output:         output,
		exited:         make(chan struct{}),
		outputRead:     make(chan struct{}),
		failpointArmed: environment["FAILPOINT"] != "",
	}

	if err := command.Start(); err != nil {
		_ = pipe.Close()
		_ = writer.Close()
		e.t.Fatalf("service could not start: %v", err)
	}
	_ = writer.Close()
	e.t.Cleanup(instance.shutdown)

	addresses := make(chan string, 1)
	go instance.readOutput(pipe, addresses)
	go instance.supervise()

	select {
	case address := <-addresses:
		instance.BaseURL = "http://" + address
	case <-instance.exited:
		e.t.Fatalf("service exited before reporting its address:\n%s", sanitise(output.String()))
	case <-time.After(readyDeadline):
		e.t.Fatalf("service did not report its address in time:\n%s", sanitise(output.String()))
	}

	instance.waitForReadiness()
	return instance
}

func (a *App) readOutput(pipe io.ReadCloser, addresses chan<- string) {
	defer close(a.outputRead)
	defer pipe.Close()

	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	reported := false

	for scanner.Scan() {
		line := scanner.Text()
		a.output.WriteString(line + "\n")

		if reported || !strings.Contains(line, "http server listening") {
			continue
		}
		var entry struct {
			Address string `json:"addr"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err == nil && entry.Address != "" {
			reported = true
			addresses <- entry.Address
		}
	}
}

func (a *App) supervise() {
	defer close(a.exited)
	err := a.command.Wait()
	<-a.outputRead

	a.mutex.Lock()
	defer a.mutex.Unlock()
	a.finished = true

	if failure := checkProcessExit(err, a.output.String(), a.killExpected || a.failpointArmed, a.stopExpected); failure != nil {
		a.t.Errorf("%v:\n%s", failure, sanitise(a.output.String()))
	}
}

func checkProcessExit(err error, output string, expectKill bool, expectStop bool) error {
	var exit *exec.ExitError
	if strings.Contains(output, "DATA RACE") || (errors.As(err, &exit) && exit.ExitCode() == raceExitCode) {
		return errors.New("the race detector reported a problem in a service process")
	}
	if err == nil && expectStop {
		return nil
	}
	if expectKill && exit != nil {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGKILL {
			return nil
		}
	}
	return fmt.Errorf("a service process stopped unexpectedly (%v)", err)
}

func (a *App) waitForReadiness() {
	a.t.Helper()

	deadline := time.Now().Add(readyDeadline)
	for time.Now().Before(deadline) {
		response, err := a.Client.Get(a.BaseURL + "/health/ready")
		if err == nil {
			readiness := response.StatusCode
			response.Body.Close()
			if readiness == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.t.Fatalf("service did not become ready:\n%s", sanitise(a.output.String()))
}

// Kill stops the process the way a crash would, which the recovery scenarios need.
func (a *App) Kill() {
	a.t.Helper()

	a.mutex.Lock()
	a.killExpected = true
	a.mutex.Unlock()

	if a.command.Process != nil {
		_ = a.command.Process.Signal(syscall.SIGKILL)
	}
	<-a.exited
}

// Stop asks for a graceful shutdown, which is what SIGTERM does in production.
func (a *App) Stop() {
	a.t.Helper()

	a.mutex.Lock()
	a.stopExpected = true
	a.mutex.Unlock()

	if a.command.Process != nil {
		_ = a.command.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-a.exited:
	case <-time.After(30 * time.Second):
		a.t.Errorf("service did not stop after SIGTERM:\n%s", sanitise(a.output.String()))
		_ = a.command.Process.Signal(syscall.SIGKILL)
		<-a.exited
	}
}

func (a *App) Running() bool {
	select {
	case <-a.exited:
		return false
	default:
		return true
	}
}

func (a *App) Output() string {
	return sanitise(a.output.String())
}

func (a *App) shutdown() {
	a.mutex.Lock()
	finished := a.finished
	a.mutex.Unlock()

	if finished {
		return
	}
	a.Stop()
}

// Request runs an authenticated call and returns the status and the decoded body,
// so scenarios can assert on the contract instead of plumbing.
func (a *App) Request(method string, path string, token string, body any, headers ...string) (int, map[string]any) {
	a.t.Helper()
	status, decoded, _ := a.Call(method, path, token, body, headers...)
	return status, decoded
}

// Call is Request plus the response headers, which the contract needs for
// Retry-After and the correlation identifier.
func (a *App) Call(method string, path string, token string, body any, headers ...string) (int, map[string]any, http.Header) {
	a.t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("request body could not be encoded: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, payload)
	if err != nil {
		a.t.Fatalf("request could not be built: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}

	response, err := a.Client.Do(request)
	if err != nil {
		a.t.Fatalf("request to %s failed: %v", path, err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		a.t.Fatalf("response from %s could not be read: %v", path, err)
	}

	decoded := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			a.t.Fatalf("response from %s is not a json object: %s", path, raw)
		}
	}
	return response.StatusCode, decoded, response.Header
}

type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(payload []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(payload)
}

func (b *syncBuffer) WriteString(text string) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.buffer.WriteString(text)
}

func (b *syncBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

// FailedStart is what a scenario learns when the service refuses to come up.
type FailedStart struct {
	ExitCode int
	Output   string
}

// StartFailingApp expects the process to refuse to start, which is how the
// scenarios prove the lifecycle validates its dependencies.
func (e *Env) StartFailingApp(options ...AppOption) FailedStart {
	e.t.Helper()

	environment := e.baseEnvironment(0)
	for _, option := range options {
		option(environment)
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyDeadline)
	defer cancel()
	command := exec.CommandContext(ctx, e.suite.binary)
	command.Env = os.Environ()
	for name, value := range environment {
		command.Env = append(command.Env, name+"="+value)
	}

	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		e.t.Fatalf("the negative startup test did not exit within its deadline:\n%s", sanitise(string(output)))
	}
	if err == nil {
		_ = command.Process.Kill()
		e.t.Fatalf("the service started even though a dependency was missing:\n%s", sanitise(string(output)))
	}

	exitCode := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		exitCode = exit.ExitCode()
	}
	if exitCode == raceExitCode || strings.Contains(string(output), "DATA RACE") {
		e.t.Fatalf("the race detector reported a problem during the negative startup test:\n%s", sanitise(string(output)))
	}
	return FailedStart{ExitCode: exitCode, Output: sanitise(string(output))}
}
