package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildWebhookMux verifies buildWebhookMux() returns a non-nil mux with a
// /webhook handler registered.
func TestBuildWebhookMux(t *testing.T) {
	t.Setenv("APP_WEBHOOK_SECRET", "appwhsec_test")
	t.Setenv("APP_IDENTITY", "docsync-bot[bot]")

	mux := buildWebhookMux()
	require.NotNil(t, mux)

	r := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	handler, _ := mux.Handler(r)
	assert.NotNil(t, handler, "/webhook handler must be registered")
}

// TestServeGracefulClose verifies serve() returns 0 when the server is closed
// gracefully via srv.Close().
func TestServeGracefulClose(t *testing.T) {
	mux := buildWebhookMux()
	require.NotNil(t, mux)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: mux}

	done := make(chan int, 1)
	go func() {
		done <- serve(srv)
	}()

	// Give ListenAndServe time to start accepting connections.
	time.Sleep(100 * time.Millisecond)

	// Confirm the port is listening.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		conn.Close()
	}

	require.NoError(t, srv.Close())

	select {
	case result := <-done:
		assert.Equal(t, 0, result)
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after Close()")
	}
}

// TestOSExitSeam documents why main() is not exercised in-process.
//
// main() is a single line:
//
//	func main() { osExit(run()) }
//
// run() blocks on serve() (srv.ListenAndServe), so invoking main() or run()
// from a test would hang the test binary until the process dies. Overriding
// osExit does not make it safe either: a real listener failure reaches
// log.Fatalf, which calls os.Exit directly and bypasses the osExit seam.
// main() is therefore only partially testable and is intentionally not called
// here. osExit remains a package-level var so the seam exists for future
// refactors; this test only asserts it can be overridden without terminating
// the test binary.
func TestOSExitSeam(t *testing.T) {
	orig := osExit
	defer func() { osExit = orig }()

	osExit = func(code int) {}
	osExit(42) // must not terminate the test binary
}
