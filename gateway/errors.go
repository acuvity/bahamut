package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/mailgun/multibuf"
	"github.com/vulcand/oxy/v2/connlimit"
	"go.acuvity.ai/elemental"
)

var (
	errLocked = elemental.NewError(
		"Service Locked",
		"The requested service is in maintenance. Please try again in a moment.",
		"gateway",
		http.StatusLocked,
	)

	errServiceUnavailable = elemental.NewError(
		"Service Temporarily Unavailable",
		"The requested service is not available. Please try again in a moment.",
		"gateway",
		http.StatusServiceUnavailable,
	)

	errGatewayTimeout = elemental.NewError(
		"Gateway Timeout",
		"The requested service took too long to respond. Please try again in a moment.",
		"gateway",
		http.StatusGatewayTimeout,
	)

	errBadGateway = elemental.NewError(
		"Bad Gateway",
		"The requested service is not available. Please try again in a moment.",
		"gateway",
		http.StatusBadGateway,
	)

	errClientClosedConnection = elemental.NewError(
		"Client Closed Connection",
		"The client closed the connection before it could complete.",
		"gateway",
		499,
	)

	errRateLimit = elemental.NewError(
		"Too Many Requests",
		"Please retry in a moment.",
		"gateway",
		http.StatusTooManyRequests,
	)

	errConnLimit = elemental.NewError(
		"Too Many Connections",
		"Please retry in a moment.",
		"gateway",
		http.StatusTooManyRequests,
	)
)

func makeError(code int, title string, description string) elemental.Error {
	return elemental.NewError(
		title,
		description,
		"gateway",
		code,
	)
}

type errorHeaderInjector func(w http.ResponseWriter, r *http.Request) http.Header

type errorHandler struct {
	corsOriginInjector errorHeaderInjector
}

func (s *errorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, err error) {

	if err == nil {
		return
	}

	slog.Debug("Handling http error", "err", err)

	if s.corsOriginInjector != nil {
		s.corsOriginInjector(w, r)
	}

	switch e := err.(type) { // nolint: errorlint

	case net.Error:

		// Better logging that in any case. A silent TLS error is worst that anything else.
		if strings.Contains(err.Error(), "tls: ") {
			writeError(w, r, makeError(http.StatusInternalServerError, "TLS Error", err.Error()))
			return
		}

		if e.Timeout() {
			writeError(w, r, errGatewayTimeout)
			return
		}

		writeError(w, r, errBadGateway)
		return

	case *connlimit.MaxConnError:
		writeError(w, r, errConnLimit)
		return

	case *multibuf.MaxSizeReachedError:
		writeError(w, r, makeError(http.StatusRequestEntityTooLarge, "Entity Too Large", fmt.Sprintf("Payload size exceeds the maximum allowed size (%d bytes)", e.MaxSize)))
		return

	case x509.UnknownAuthorityError, x509.HostnameError, x509.CertificateInvalidError, x509.ConstraintViolationError, *tls.CertificateVerificationError, tls.AlertError, tls.RecordHeaderError, *tls.RecordHeaderError:
		writeError(w, r, makeError(495, "TLS Error", err.Error()))
		return
	}

	switch {
	case errors.Is(err, io.EOF):
		writeError(w, r, errBadGateway)
	case errors.Is(err, context.Canceled):
		writeError(w, r, errClientClosedConnection)
	case errors.Is(err, errTooManyRequest):
		writeError(w, r, errRateLimit)
	default:

		// the http package function MaxBytesReader is returning an error.erroString
		// so we need to check its string value.
		if err.Error() == "http: request body too large" {
			writeError(w, r, makeError(http.StatusRequestEntityTooLarge, "Entity Too Large", err.Error()))
			return
		}

		writeError(w, r, makeError(http.StatusInternalServerError, "Internal Server Error", err.Error()))
	}
}

func writeError(w http.ResponseWriter, r *http.Request, eerr elemental.Error) {

	_, encoding, err := elemental.EncodingFromHeaders(r.Header)
	if err != nil {
		encoding = elemental.EncodingTypeJSON
	}

	data, err := elemental.Encode(encoding, elemental.NewErrors(eerr))
	if err != nil {
		http.Error(w, "Error while encoding the error", eerr.Code)
	}

	if encoding == elemental.EncodingTypeJSON {
		w.Header().Set("Content-Type", string(encoding)+"; charset=UTF-8")
	} else {
		w.Header().Set("Content-Type", string(encoding))
	}

	w.WriteHeader(eerr.Code)
	w.Write(data) // nolint
}
