package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"

	"github.com/armon/go-proxyproto"
	"github.com/vulcand/oxy/v2/buffer"
	"github.com/vulcand/oxy/v2/cbreaker"
	"github.com/vulcand/oxy/v2/connlimit"
	"github.com/vulcand/oxy/v2/forward"
	"github.com/vulcand/oxy/v2/utils"
	"go.acuvity.ai/bahamut"
)

// An gateway is cool
type gateway struct {
	server                 *http.Server
	upstreamer             Upstreamer
	upstreamerLatency      LatencyBasedUpstreamer
	forwarder              *httputil.ReverseProxy
	proxyHTTPHandler       http.Handler
	proxyWSHandler         http.Handler
	listener               net.Listener
	goodbyeServer          *http.Server
	gatewayConfig          *gwconfig
	corsOriginInjectorFunc func(w http.ResponseWriter, r *http.Request) http.Header
}

// New returns a new Gateway.
func New(listenAddr string, upstreamer Upstreamer, options ...Option) (Gateway, error) {

	cfg := newGatewayConfig()
	for _, o := range options {
		o(cfg)
	}

	var listener net.Listener

	rootListener, err := bahamut.MakeListener("tcp4", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("unable build fast tcp listener: %w", err)
	}

	if cfg.tcpGlobalRateLimitingEnabled {
		rootListener = newLimitedListener(
			rootListener,
			cfg.tcpGlobalRateLimitingCPS,
			cfg.tcpGlobalRateLimitingBurst,
			cfg.tcpGlobalRateLimitingMetricManager,
		)
	}

	if cfg.proxyProtocolEnabled {

		sc, err := makeProxyProtocolSourceChecker(cfg.proxyProtocolSubnet)
		if err != nil {
			return nil, fmt.Errorf("unable build proxy protocol source checker: %w", err)
		}

		if cfg.serverTLSConfig != nil {
			listener = tls.NewListener(
				&proxyproto.Listener{
					Listener:    rootListener,
					SourceCheck: sc,
				},
				cfg.serverTLSConfig,
			)
		} else {
			listener = &proxyproto.Listener{
				Listener:    rootListener,
				SourceCheck: sc,
			}
		}

	} else {
		if cfg.serverTLSConfig != nil {
			listener = tls.NewListener(rootListener, cfg.serverTLSConfig)
		} else {
			listener = rootListener
		}
	}

	var serverLogger *log.Logger
	if !cfg.trace {
		serverLogger = slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug)
	}

	s := &gateway{
		goodbyeServer: makeGoodbyeServer(listenAddr, cfg.serverTLSConfig),
		listener:      listener,
		upstreamer:    upstreamer,
		gatewayConfig: cfg,
	}

	if u, ok := s.upstreamer.(LatencyBasedUpstreamer); ok {
		s.upstreamerLatency = u
	}

	s.server = &http.Server{
		ReadTimeout:  cfg.httpReadTimeout,
		WriteTimeout: cfg.httpWriteTimeout,
		IdleTimeout:  cfg.httpIdleTimeout,
		ErrorLog:     serverLogger,
		Handler:      s,
		ConnState: func(conn net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				if mm := cfg.metricsManager; mm != nil {
					mm.RegisterTCPConnection()
				}
			case http.StateClosed, http.StateHijacked:
				if mm := cfg.metricsManager; mm != nil {
					mm.UnregisterTCPConnection()
				}
			}
		},
	}

	s.corsOriginInjectorFunc = func(w http.ResponseWriter, r *http.Request) http.Header {
		return injectCORSHeader(
			w.Header(),
			cfg.corsOrigin,
			cfg.additionalCorsOrigin,
			cfg.corsAllowCredentials,
			r.Header.Get("origin"),
			r.Method,
		)
	}

	var (
		topProxyHTTPHandler http.Handler
		topProxyWSHandler   http.Handler
	)

	s.forwarder = forward.New(true)
	s.forwarder.BufferPool = newPool(1024 * 1024)
	s.forwarder.ErrorHandler = (&errorHandler{corsOriginInjector: s.corsOriginInjectorFunc}).ServeHTTP
	s.forwarder.Director = nil
	s.forwarder.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		ForceAttemptHTTP2:   cfg.upstreamUseHTTP2,
		TLSClientConfig:     cfg.upstreamTLSConfig,
		DisableCompression:  !cfg.upstreamEnableCompression,
		MaxConnsPerHost:     cfg.upstreamMaxConnsPerHost,
		MaxIdleConns:        cfg.upstreamMaxIdleConns,
		MaxIdleConnsPerHost: cfg.upstreamMaxIdleConnsPerHost,
		TLSHandshakeTimeout: cfg.upstreamTLSHandshakeTimeout,
		IdleConnTimeout:     cfg.upstreamIdleConnTimeout,
	}
	s.forwarder.Rewrite = (&requestRewriter{
		blockOpenTracing:   (!cfg.exposePrivateAPIs && cfg.blockOpenTracingHeaders),
		private:            cfg.exposePrivateAPIs,
		customRewriter:     cfg.requestRewriter,
		trustForwardHeader: cfg.trustForwardHeader,
	}).Rewrite
	s.forwarder.ModifyResponse = func(resp *http.Response) error {

		if resp.Request == nil {
			return nil
		}

		injectCORSHeader(
			resp.Header,
			cfg.corsOrigin,
			cfg.additionalCorsOrigin,
			cfg.corsAllowCredentials,
			resp.Request.Header.Get("origin"),
			resp.Request.Method,
		)

		if s.gatewayConfig.responseRewriter != nil {
			if err := s.gatewayConfig.responseRewriter(resp); err != nil {
				return fmt.Errorf("unable to execute response rewriter: %w", err)
			}
		}
		return nil
	}

	topProxyHTTPHandler = s.forwarder
	topProxyWSHandler = s.forwarder

	if topProxyHTTPHandler, err = buffer.New(
		topProxyHTTPHandler,
		// buffer.MaxRequestBodyBytes(1024*1024),
		// buffer.MemRequestBodyBytes(1024*1024*1024),
		buffer.ErrorHandler(&errorHandler{corsOriginInjector: s.corsOriginInjectorFunc}),
	); err != nil {
		return nil, fmt.Errorf("unable to initialize request buffer: %w", err)
	}

	if cfg.tcpClientMaxConnectionsEnabled {

		if topProxyHTTPHandler, err = connlimit.New(
			topProxyHTTPHandler,
			utils.ExtractorFunc(func(req *http.Request) (token string, amount int64, err error) {
				token, err = cfg.tcpClientSourceExtractor.ExtractSource(req)
				return token, 1, err
			}),
			int64(cfg.tcpClientMaxConnections),
			connlimit.ErrorHandler(&errorHandler{corsOriginInjector: s.corsOriginInjectorFunc}),
		); err != nil {
			return nil, fmt.Errorf("unable to initialize connection limiter: %w", err)
		}
	}

	if cfg.sourceRateLimitingEnabled {
		srcLimiter := newSourceLimiter(
			topProxyHTTPHandler,
			topProxyWSHandler,
			cfg.sourceRateLimitingRPS,
			cfg.sourceRateLimitingBurst,
			cfg.sourceExtractor,
			cfg.sourceRateExtractor,
			&errorHandler{corsOriginInjector: s.corsOriginInjectorFunc},
			cfg.sourceRateLimitingMetricManager,
		)
		topProxyHTTPHandler = srcLimiter
		topProxyWSHandler = srcLimiter
	}

	if cfg.upstreamCircuitBreakerCond != "" {
		if topProxyHTTPHandler, err = cbreaker.New(
			topProxyHTTPHandler,
			cfg.upstreamCircuitBreakerCond,
			cbreaker.Fallback(&circuitBreakerHandler{}),
		); err != nil {
			return nil, fmt.Errorf("unable to initialize circuit breaker: %w", err)
		}
	}

	s.proxyHTTPHandler = topProxyHTTPHandler
	s.proxyWSHandler = topProxyWSHandler

	return s, nil
}

// Start starts the http server
func (s *gateway) Start() {

	go func() {

		if err := s.server.Serve(s.listener); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			slog.Error("Unable to start internal API server", err)
			os.Exit(1)
		}
	}()
}

func (s *gateway) Stop() {

	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	// Stopping main server
	go func() {
		defer cancel()
		if err := s.server.Shutdown(stopCtx); err != nil {
			slog.Error("Could not gracefully stop internal API server", err)
		} else {
			slog.Debug("Internet API server stopped")
		}
	}()

	// We start a temporary server to tell the world we are not serving requests anymore
	// We due this due to kubernetes continuing service traffic to the terminating pod.
	// As nobody responds anymore while nginx finishes treating the requests, this leads
	// to connection timeout, with mostly no chance of retrying.
	// This server makes sure we return immediately with a retryable error.
	go func() {
		slog.Info("Starting temporary redirect server...")
		for {
			if err := s.goodbyeServer.ListenAndServeTLS("", ""); err != nil {
				if strings.Contains(err.Error(), "address already in use") {
					continue
				}
				if errors.Is(err, http.ErrServerClosed) {
					return
				}
				slog.Error("Unable to start temporary redirect server", err)
				return
			}
		}
	}()

	<-stopCtx.Done()

	stopCtx, cancel = context.WithTimeout(context.Background(), 1*time.Second)
	go func() {
		defer cancel()
		slog.Info("Stopping temporary redirect server...")
		if err := s.goodbyeServer.Shutdown(stopCtx); err != nil {
			slog.Error("Could not gracefully stop temp server", err)
		}
	}()

	<-stopCtx.Done()
}

func (s *gateway) checkInterceptor(
	registry map[string]InterceptorFunc,
	checker func(string, string) bool,
	w http.ResponseWriter,
	r *http.Request,
	path string,
) (InterceptorAction, string, error) {

	cfg := s.gatewayConfig

	for key, interceptor := range registry {

		if !checker(path, key) {
			continue
		}

		return interceptor(w, r, writeError, func() {
			injectCORSHeader(
				w.Header(),
				cfg.corsOrigin,
				cfg.additionalCorsOrigin,
				cfg.corsAllowCredentials,
				r.Header.Get("origin"),
				r.Method,
			)
		})
	}

	return 0, "", nil
}

func (s *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	injectGeneralHeader(w.Header())

	if r.Method == http.MethodOptions {
		h := w.Header()
		injectCORSHeader(
			h,
			s.gatewayConfig.corsOrigin,
			s.gatewayConfig.additionalCorsOrigin,
			s.gatewayConfig.corsAllowCredentials,
			r.Header.Get("Origin"),
			r.Method,
		)
		w.WriteHeader(http.StatusOK) // nolint: errcheck
		return
	}

	if s.gatewayConfig.maintenance {
		h := w.Header()
		h.Set("Content-Type", "application/msgpack, application/json")
		injectCORSHeader(
			h,
			s.gatewayConfig.corsOrigin,
			s.gatewayConfig.additionalCorsOrigin,
			s.gatewayConfig.corsAllowCredentials,
			r.Header.Get("Origin"),
			r.Method,
		)
		writeError(w, r, errLocked)
		return
	}

	path := r.URL.Path

	var upstream string
	var interceptAction InterceptorAction
	var err error

	for _, interceptor := range s.gatewayConfig.interceptors {
		interceptAction, upstream, err = interceptor(w, r, writeError, func() {
			injectCORSHeader(
				w.Header(),
				s.gatewayConfig.corsOrigin,
				s.gatewayConfig.additionalCorsOrigin,
				s.gatewayConfig.corsAllowCredentials,
				r.Header.Get("origin"),
				r.Method,
			)
		})
		if interceptAction != 0 {
			goto HANDLE_INTERCEPTION
		}
	}

	// First we look for the exact match
	if interceptAction, upstream, err = s.checkInterceptor(
		s.gatewayConfig.exactInterceptors,
		func(path string, key string) bool { return path == key },
		w, r, path,
	); interceptAction != 0 {
		goto HANDLE_INTERCEPTION
	}

	// If we reach here, we check for prefix match
	if interceptAction, upstream, err = s.checkInterceptor(
		s.gatewayConfig.prefixInterceptors,
		func(path string, key string) bool { return strings.HasPrefix(path, key) },
		w, r, path,
	); interceptAction != 0 {
		goto HANDLE_INTERCEPTION
	}

	// If we reach here, we check for suffix match
	if interceptAction, upstream, err = s.checkInterceptor(
		s.gatewayConfig.suffixInterceptors,
		func(path string, key string) bool { return strings.HasSuffix(path, key) },
		w, r, path,
	); interceptAction != 0 {
		goto HANDLE_INTERCEPTION
	}

HANDLE_INTERCEPTION:
	if err != nil {
		writeError(w, r, makeError(http.StatusInternalServerError, "Internal Server Error", fmt.Sprintf("unable to run interceptor: %s", err)))
		return
	}
	if interceptAction == InterceptorActionStop {
		// This has no incidence if the interceptor already wrote the header.
		// In such case caller must call the corsInjector by himself.
		injectCORSHeader(
			w.Header(),
			s.gatewayConfig.corsOrigin,
			s.gatewayConfig.additionalCorsOrigin,
			s.gatewayConfig.corsAllowCredentials,
			r.Header.Get("Origin"),
			r.Method,
		)
		return
	}

	// If we don't have an upstream returned by an interceptor,
	// we find it as usual.
	if upstream == "" {

		if upstream, err = s.upstreamer.Upstream(r); err != nil {

			switch {

			case errors.Is(err, ErrUpstreamerTooManyRequests):

				if mm := s.gatewayConfig.metricsManager; mm != nil {
					mm.MeasureRequest(r.Method, path)(http.StatusTooManyRequests, nil)
				}
				s.corsOriginInjectorFunc(w, r)
				writeError(w, r, errRateLimit)

			default:

				slog.Error("Upstreamer error",
					"ip", r.RemoteAddr,
					"method", r.Method,
					"proto", r.Proto,
					"path", r.URL.Path,
					"ns", r.Header.Get("X-Namespace"),
					"routed", upstream,
					"scheme", s.gatewayConfig.upstreamURLScheme,
					err,
				)

				s.corsOriginInjectorFunc(w, r)
				writeError(w, r, makeError(http.StatusInternalServerError, "Internal Server Error", err.Error()))
			}

			return
		}

		if upstream == "" {
			s.corsOriginInjectorFunc(w, r)
			writeError(w, r, errServiceUnavailable)
			return
		}
	}

	slog.Debug("request",
		"ip", r.RemoteAddr,
		"method", r.Method,
		"proto", r.Proto,
		"path", r.URL.Path,
		"ns", r.Header.Get("X-Namespace"),
		"routed", upstream,
		"scheme", s.gatewayConfig.upstreamURLScheme,
	)

	r.URL.Host = upstream
	r.URL.Scheme = s.gatewayConfig.upstreamURLScheme

	// Always strip the internal ws header marker
	// to make sure it cannot be sent by the clients.
	r.Header.Del(internalWSMarkingHeader)

	switch interceptAction {

	case InterceptorActionForwardWS:

		if mm := s.gatewayConfig.metricsManager; mm != nil {
			mm.RegisterWSConnection()
		}

		// We mark the request as a websocket so the
		// rewriter can handle settinfg X-Forwarded-For header
		// See rewriter for more info.
		r.Header.Set(internalWSMarkingHeader, "1")

		s.proxyWSHandler.ServeHTTP(w, r)

		if mm := s.gatewayConfig.metricsManager; mm != nil {
			mm.UnregisterWSConnection()
		}

	case InterceptorActionForwardDirect:

		var finish bahamut.FinishMeasurementFunc

		if mm := s.gatewayConfig.metricsManager; mm != nil {
			finish = mm.MeasureRequest(r.Method, path)
		}

		s.forwarder.ServeHTTP(w, r)

		if finish != nil {
			rt := finish(0, nil)
			if s.upstreamerLatency != nil {
				s.upstreamerLatency.CollectLatency(upstream, rt)
			}
		}

	default:

		var finish bahamut.FinishMeasurementFunc

		if mm := s.gatewayConfig.metricsManager; mm != nil {
			finish = mm.MeasureRequest(r.Method, path)
		}

		s.proxyHTTPHandler.ServeHTTP(w, r)

		if finish != nil {
			rt := finish(0, nil)
			if s.upstreamerLatency != nil {
				s.upstreamerLatency.CollectLatency(upstream, rt)
			}
		}
	}
}
