// Package devmock is the offline sidecar for `actions dev --offline`:
// same routes as the control-plane sidecar, all state in memory.
package devmock

import (
	"net"
	"net/http"

	"github.com/ur-wesley/serverless/internal/artifacts"
	"github.com/ur-wesley/serverless/internal/bus"
	"github.com/ur-wesley/serverless/internal/sidecar"
)

type Mock struct {
	Server *sidecar.Server
	Arts   *artifacts.MemStore
	Bus    bus.Bus
	mux    *http.ServeMux
}

func New() *Mock {
	arts := artifacts.NewMemory()
	b := bus.NewMemory()
	srv := &sidecar.Server{
		KV:   sidecar.NewMemKV(),
		Bus:  b,
		Logs: sidecar.NewLogRing(),
	}
	m := &Mock{Server: srv, Arts: arts, Bus: b, mux: http.NewServeMux()}
	srv.Blobs = &sidecar.LocalBlob{} // ControlURL set in Listen (self URL)
	srv.RegisterRoutes(m.mux)
	sidecar.RegisterTransferRoutes(m.mux, arts, nil)
	return m
}

// Listen starts the mock on 127.0.0.1:0 and returns its base URL.
func (m *Mock) Listen() (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	lb := m.Server.Blobs.(*sidecar.LocalBlob)
	lb.ControlURL = "http://" + l.Addr().String()
	go http.Serve(l, m.mux) //nolint:errcheck
	return lb.ControlURL, func() { l.Close() }, nil
}
