package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_CMD_APP_ServeGracefulClose(t *testing.T) {
	port := freePort(t)
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})}

	done := make(chan int, 1)
	go func() { done <- serve(srv) }()

	require.NoError(t, httpGetRetry(t, "http://127.0.0.1:"+port+"/"))

	require.NoError(t, srv.Close())
	assert.Equal(t, 0, <-done, "serve returns 0 on graceful close")
}
