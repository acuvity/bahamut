// Copyright 2019 Aporeto Inc.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bahamut

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// an profilingServer is the structure serving the profiling.
type profilingServer struct {
	server *http.Server
	cfg    config
}

// newProfilingServer returns a new profilingServer.
func newProfilingServer(cfg config) *profilingServer {

	return &profilingServer{
		cfg: cfg,
	}
}

// start starts the profilingServer.
func (s *profilingServer) start(ctx context.Context) {

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	s.server = &http.Server{
		Addr:    s.cfg.profilingServer.listenAddress,
		Handler: mux,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil {
			if err == http.ErrServerClosed {
				return
			}
			slog.Error("Unable to start profiling server", err)
			os.Exit(1)
		}
	}()

	slog.Info("Profiler server started", "address", s.cfg.profilingServer.listenAddress)

	<-ctx.Done()
}

// stop stops the profilingServer.
func (s *profilingServer) stop() {

	if s.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)

	go func() {
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			slog.Error("Could not gracefully stop profiling server", err)
		} else {
			slog.Debug("Profiling server stopped")
		}
	}()

	slog.Debug("Profile server stopped")
}
