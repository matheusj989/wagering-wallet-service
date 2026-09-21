//go:build integration

package testenv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// DatabaseProxy sits between the service and PostgreSQL so a scenario can drop the
// answer of a statement the server already executed. That is the only honest way to
// reproduce a commit whose outcome the client never learns.
type DatabaseProxy struct {
	t        *testing.T
	listener net.Listener
	target   string
	database string

	mutex sync.Mutex
	armed string
	cuts  atomic.Int64
}

func (e *Env) StartDatabaseProxy() *DatabaseProxy {
	e.t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatalf("the database proxy could not listen: %v", err)
	}

	proxy := &DatabaseProxy{
		t:        e.t,
		listener: listener,
		target:   e.suite.postgres.address(),
		database: e.database,
	}
	e.t.Cleanup(func() { _ = listener.Close() })

	go proxy.accept()
	return proxy
}

// URL is the connection string the service should use to reach PostgreSQL through
// the proxy.
func (p *DatabaseProxy) URL() string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		appRole, appPassword, p.listener.Addr().String(), p.database)
}

// CutAfter arms the proxy: the next server answer whose command tag starts with the
// given word is executed by PostgreSQL and then dropped, taking the connection with
// it. "COMMIT" reproduces an unknown commit outcome, "INSERT" an abort before it.
func (p *DatabaseProxy) CutAfter(commandTag string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.armed = commandTag
}

func (p *DatabaseProxy) Cuts() int64 {
	return p.cuts.Load()
}

func (p *DatabaseProxy) shouldCut(tag string) bool {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.armed == "" || !strings.HasPrefix(tag, p.armed) {
		return false
	}
	p.armed = ""
	p.cuts.Add(1)
	return true
}

func (p *DatabaseProxy) accept() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.handle(client)
	}
}

func (p *DatabaseProxy) handle(client net.Conn) {
	server, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}

	closeBoth := sync.OnceFunc(func() {
		_ = client.Close()
		_ = server.Close()
	})

	go func() {
		defer closeBoth()
		_, _ = io.Copy(server, client)
	}()

	defer closeBoth()
	p.forwardAnswers(server, client)
}

// forwardAnswers copies the server stream to the client, message by message, so the
// proxy can recognise a command tag and drop that answer when it is armed.
func (p *DatabaseProxy) forwardAnswers(server net.Conn, client net.Conn) {
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(server, header); err != nil {
			return
		}
		length := binary.BigEndian.Uint32(header[1:5])
		if length < 4 {
			return
		}

		payload := make([]byte, length-4)
		if _, err := io.ReadFull(server, payload); err != nil && !errors.Is(err, io.EOF) {
			return
		}

		if header[0] == 'C' {
			tag := strings.TrimRight(string(payload), "\x00")
			if p.shouldCut(tag) {
				return
			}
		}

		if _, err := client.Write(header); err != nil {
			return
		}
		if _, err := client.Write(payload); err != nil {
			return
		}
	}
}
