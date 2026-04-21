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
	"net/http"
	"time"

	opentracing "github.com/opentracing/opentracing-go"
)

// FinishMeasurementFunc is the kind of functinon returned by MetricsManager.MeasureRequest().
type FinishMeasurementFunc func(code int, span opentracing.Span) time.Duration

// A MetricsManager handles Prometheus Metrics Management
type MetricsManager interface {

	// MeasureRequest can be used to measure bahamut request. The url will be
	// sanitized and eventual id will be switched to :id
	MeasureRequest(method string, path string) FinishMeasurementFunc

	// MeasureGenericRequest measures an http request without further sanitization.
	// This can be used to measure random url.
	MeasureGenericRequest(method string, path string) FinishMeasurementFunc

	// RegisterWSConnection will increment the counter of current websocket connections
	RegisterWSConnection()

	// UnregisterWSConnection deacreases the counter of currently active websocket connections.
	UnregisterWSConnection()

	// RegisterTCPConnection will increment the counter of current TCP connections
	RegisterTCPConnection()

	// UnregisterTCPConnection deacreases the counter of currently active TCP connections.
	UnregisterTCPConnection()

	// Write writes the state of the measurement into the response writer.
	Write(w http.ResponseWriter, r *http.Request)
}
