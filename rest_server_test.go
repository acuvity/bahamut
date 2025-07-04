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
	"crypto/tls"
	"crypto/x509"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-zoo/bone"
	opentracing "github.com/opentracing/opentracing-go"
	. "github.com/smartystreets/goconvey/convey"
	"go.acuvity.ai/elemental"
	testmodel "go.acuvity.ai/elemental/test/model"
	"golang.org/x/time/rate"
)

func loadFixtureCertificates() (*x509.CertPool, []tls.Certificate) {

	clientCACertData, _ := os.ReadFile("fixtures/certs/ca-cert.pem")
	clientCAPool := x509.NewCertPool()
	clientCAPool.AppendCertsFromPEM(clientCACertData)

	serverCert, _ := tls.LoadX509KeyPair("fixtures/certs/server-cert.pem", "fixtures/certs/server-key.pem")
	return clientCAPool, []tls.Certificate{serverCert}
}

func TestServer_Initialization(t *testing.T) {

	Convey("Given I create a new api server", t, func() {

		cfg := config{}
		cfg.restServer.listenAddress = "address:80"

		c := newRestServer(cfg, bone.New(), nil, nil, nil)

		Convey("Then it should be correctly initialized", func() {
			So(len(c.multiplexer.Routes), ShouldEqual, 0)
			So(c.cfg, ShouldResemble, cfg)
		})
	})
}

func TestServer_createSecureHTTPServer(t *testing.T) {

	Convey("Given I create a new api server without all valid tls info", t, func() {

		clientcapool, servercerts := loadFixtureCertificates()

		cfg := config{}
		cfg.restServer.listenAddress = "address:80"
		cfg.tls.clientCAPool = clientcapool
		cfg.tls.serverCertificates = servercerts
		cfg.tls.authType = tls.RequireAndVerifyClientCert

		c := newRestServer(cfg, bone.New(), nil, nil, nil)

		Convey("When I make a secure server", func() {
			srv := c.createSecureHTTPServer(cfg.restServer.listenAddress)

			Convey("Then the server should be correctly initialized", func() {
				So(srv, ShouldNotBeNil)
			})
		})
	})

	Convey("Given I create a new api server without all custom tls cert func", t, func() {

		r := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, nil }

		cfg := config{}
		cfg.restServer.listenAddress = "address:80"
		cfg.tls.serverCertificatesRetrieverFunc = r
		c := newRestServer(cfg, bone.New(), nil, nil, nil)

		Convey("When I make a secure server", func() {
			srv := c.createSecureHTTPServer(cfg.restServer.listenAddress)

			Convey("Then the server should be correctly initialized", func() {
				So(srv.TLSConfig.GetCertificate, ShouldEqual, r)
			})
		})
	})
}

func TestServer_createUnsecureHTTPServer(t *testing.T) {

	Convey("Given I create a new api server without any tls info", t, func() {

		cfg := config{}
		cfg.restServer.listenAddress = "address:80"

		c := newRestServer(cfg, bone.New(), nil, nil, nil)

		Convey("When I make an unsecure server", func() {
			srv := c.createUnsecureHTTPServer(cfg.restServer.listenAddress)

			Convey("Then the server should be correctly initialized", func() {
				So(srv, ShouldNotBeNil)
			})
		})
	})
}

func TestServer_RouteInstallation(t *testing.T) {

	Convey("Given I create a new api server with routes", t, func() {

		routes := map[int][]RouteInfo{
			1: {
				{
					URL:   "/a",
					Verbs: []string{"GET"},
				},
			},
			2: {
				{
					URL:   "/b",
					Verbs: []string{"POST"},
				},
				{
					URL:   "/c/d",
					Verbs: []string{"POST", "GET"},
				},
			},
		}

		cfg := config{}
		cfg.restServer.customRootHandlerFunc = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		cfg.restServer.listenAddress = "address:80"
		cfg.meta.serviceName = "hello"
		cfg.meta.version = map[string]any{}

		c := newRestServer(cfg, bone.New(), nil, nil, nil)

		Convey("When I install the routes", func() {

			c.installRoutes(routes)

			Convey("Then the bone Multiplexer should have correct number of handlers", func() {
				So(len(c.multiplexer.Routes[http.MethodPost]), ShouldEqual, 5)
				So(len(c.multiplexer.Routes[http.MethodGet]), ShouldEqual, 10)
				So(len(c.multiplexer.Routes[http.MethodDelete]), ShouldEqual, 3)
				So(len(c.multiplexer.Routes[http.MethodPatch]), ShouldEqual, 3)
				So(len(c.multiplexer.Routes[http.MethodHead]), ShouldEqual, 5)
				So(len(c.multiplexer.Routes[http.MethodPut]), ShouldEqual, 3)
			})
		})
	})

	Convey("Given I create a new api server with API and custom routes", t, func() {

		routes := map[int][]RouteInfo{
			1: {
				{
					URL:   "/a",
					Verbs: []string{"GET"},
				},
			},
			2: {
				{
					URL:   "/b",
					Verbs: []string{"POST"},
				},
				{
					URL:   "/c/d",
					Verbs: []string{"POST", "GET"},
				},
			},
		}

		cfg := config{}
		cfg.restServer.apiPrefix = "/api"
		cfg.restServer.customRoutePrefix = "/custom"
		cfg.restServer.customRootHandlerFunc = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		cfg.restServer.listenAddress = "address:80"
		cfg.meta.serviceName = "hello"
		cfg.meta.version = map[string]any{}
		customHandlerFunc := func() map[string]http.HandlerFunc {
			return map[string]http.HandlerFunc{
				"/saml": http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			}
		}

		c := newRestServer(cfg, bone.New(), nil, customHandlerFunc, nil)

		Convey("When I install the routes", func() {

			c.installRoutes(routes)

			Convey("Then the bone Multiplexer should have correct number of handlers", func() {
				So(len(c.multiplexer.Routes[http.MethodPost]), ShouldEqual, 6)
				So(len(c.multiplexer.Routes[http.MethodGet]), ShouldEqual, 11)
				So(len(c.multiplexer.Routes[http.MethodDelete]), ShouldEqual, 4)
				So(len(c.multiplexer.Routes[http.MethodPatch]), ShouldEqual, 4)
				So(len(c.multiplexer.Routes[http.MethodHead]), ShouldEqual, 6)
				So(len(c.multiplexer.Routes[http.MethodPut]), ShouldEqual, 4)
			})

			Convey("The routes must have the correct prefix", func() {
				routes := c.multiplexer.Routes[http.MethodGet]
				for _, route := range routes {
					if route.Path == "/" || strings.HasPrefix(route.Path, "/_meta") {
						continue
					}
					So(strings.HasPrefix(route.Path, "/api") || strings.HasPrefix(route.Path, "/custom"), ShouldBeTrue)
				}
			})
		})
	})
}

func TestServer_Start(t *testing.T) {

	Convey("Given I create an api without tls server", t, func() {

		Convey("When I start the server", func() {

			port1 := strconv.Itoa(rand.Intn(10000) + 20000)

			cfg := config{}
			cfg.restServer.listenAddress = "127.0.0.1:" + port1

			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			defer c.stop()

			go c.start(context.TODO(), nil)
			time.Sleep(30 * time.Millisecond)

			resp, err := http.Get("http://127.0.0.1:" + port1)
			if err == nil {
				defer func() { _ = resp.Body.Close() }()
			}

			Convey("Then the status code should be OK", func() {
				So(err, ShouldBeNil)
				So(resp.StatusCode, ShouldEqual, 200)
			})
		})
	})

	Convey("Given I create an api with tls server", t, func() {

		Convey("When I start the server", func() {

			port1 := strconv.Itoa(rand.Intn(10000) + 40000)

			// h := func(w http.ResponseWriter, req *http.Request) { w.Write([]byte("hello")) }

			clientcapool, servercerts := loadFixtureCertificates()

			cfg := config{}
			cfg.restServer.listenAddress = "127.0.0.1:" + port1
			cfg.tls.clientCAPool = clientcapool
			cfg.tls.serverCertificates = servercerts
			cfg.tls.authType = tls.RequireAndVerifyClientCert

			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			defer c.stop()

			go c.start(context.TODO(), nil)
			time.Sleep(30 * time.Millisecond)

			cert, _ := tls.LoadX509KeyPair("fixtures/certs/client-cert.pem", "fixtures/certs/client-key.pem")
			cacert, _ := os.ReadFile("fixtures/certs/ca-cert.pem")
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(cacert)
			tlsConfig := &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      pool,
			}

			transport := &http.Transport{TLSClientConfig: tlsConfig}
			client := &http.Client{Transport: transport}

			resp, err := client.Get("https://localhost:" + port1)
			if err == nil {
				defer func() { _ = resp.Body.Close() }()
			}

			Convey("Then the status code should be 200", func() {
				So(err, ShouldBeNil)
				So(resp.StatusCode, ShouldEqual, 200)
			})
		})
	})
}

type mockMetricsManager struct {
	measureFunc FinishMeasurementFunc
}

func (m *mockMetricsManager) MeasureRequest(method string, url string) FinishMeasurementFunc {
	return m.measureFunc
}
func (m *mockMetricsManager) RegisterWSConnection()                        {}
func (m *mockMetricsManager) UnregisterWSConnection()                      {}
func (m *mockMetricsManager) RegisterTCPConnection()                       {}
func (m *mockMetricsManager) UnregisterTCPConnection()                     {}
func (m *mockMetricsManager) Write(w http.ResponseWriter, r *http.Request) {}

func TestServer_MakeHandlers(t *testing.T) {

	Convey("Given I have some config", t, func() {

		mm := map[int]elemental.ModelManager{
			0: testmodel.Manager(),
			1: testmodel.Manager(),
		}

		var measuredCode int
		cfg := config{}
		cfg.model.modelManagers = mm
		cfg.healthServer.metricsManager = &mockMetricsManager{
			measureFunc: func(code int, span opentracing.Span) time.Duration { measuredCode = code; return 0 },
		}

		Convey("When I create a handler with a bad url", func() {
			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest(http.MethodGet, "http://toto.com/identity", nil)
			r.URL = &url.URL{}
			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusBadRequest)
			So(measuredCode, ShouldEqual, http.StatusBadRequest)
		})

		Convey("When I create a handler with badly versionned api", func() {
			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest(http.MethodGet, "http://toto.com/v/dog/identity", nil)
			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusBadRequest)
			So(w.Body.String(), ShouldEqual, `[{"code":400,"description":"Invalid api version number","subject":"bahamut","title":"Bad Request","trace":"unknown"}]`)
			So(measuredCode, ShouldEqual, http.StatusBadRequest)
		})

		Convey("When I create a handler with unknown versionned api", func() {
			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest(http.MethodGet, "http://toto.com/v/42/identity", nil)
			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusBadRequest)
			So(w.Body.String(), ShouldEqual, `[{"code":400,"description":"Unknown api version","subject":"bahamut","title":"Bad Request","trace":"unknown"}]`)
			So(measuredCode, ShouldEqual, http.StatusBadRequest)
		})

		Convey("When I create a handler without rate limiters", func() {

			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest(http.MethodGet, "http://toto.com/identity", nil)

			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusMethodNotAllowed)
			So(measuredCode, ShouldEqual, http.StatusMethodNotAllowed)
		})

		Convey("When I create a handler with global rate limiters", func() {

			cfg.rateLimiting.rateLimiter = rate.NewLimiter(rate.Limit(1), 1)

			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest(http.MethodGet, "http://toto.com/identity", nil)
			h(w, r)

			w = httptest.NewRecorder()
			r, _ = http.NewRequest(http.MethodGet, "http://toto.com/identity", nil)
			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusTooManyRequests)
			So(measuredCode, ShouldEqual, http.StatusTooManyRequests)
		})

		Convey("When I create a handler with per api rate limiters", func() {

			cfg.rateLimiting.apiRateLimiters = map[elemental.Identity]apiRateLimit{
				testmodel.ListIdentity: {
					limiter: rate.NewLimiter(rate.Limit(1), 1),
				},
			}

			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest("DOG", "http://toto.com/lists", nil) // trick to not go any further
			h(w, r)

			w = httptest.NewRecorder()
			r, _ = http.NewRequest("DOG", "http://toto.com/lists", nil) // trick to not go any further
			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusTooManyRequests)
			So(measuredCode, ShouldEqual, http.StatusTooManyRequests)
		})

		Convey("When I create a handler with per api rate limiters and ignore condition", func() {

			cfg.rateLimiting.apiRateLimiters = map[elemental.Identity]apiRateLimit{
				testmodel.ListIdentity: {
					limiter:   rate.NewLimiter(rate.Limit(1), 1),
					condition: func(*elemental.Request) bool { return false },
				},
			}

			c := newRestServer(cfg, bone.New(), nil, nil, nil)
			h := c.makeHandler(handleRetrieve)

			w := httptest.NewRecorder()
			r, _ := http.NewRequest("DOG", "http://toto.com/lists", nil) // trick to not go any further
			h(w, r)

			w = httptest.NewRecorder()
			r, _ = http.NewRequest("DOG", "http://toto.com/lists", nil) // trick to not go any further
			h(w, r)

			So(w.Result().StatusCode, ShouldEqual, http.StatusInternalServerError) //  this happens be
			So(measuredCode, ShouldEqual, http.StatusInternalServerError)
		})
	})
}
