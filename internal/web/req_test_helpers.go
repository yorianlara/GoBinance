package web

import (
	"io"
	"net/http"
	"testing"
)

func mustReq(t *testing.T, method, path string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}
