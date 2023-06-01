package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/boxo/gateway/assets"
	"github.com/ipfs/boxo/path/resolver"
	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	"github.com/ipld/go-ipld-prime/datamodel"
)

var (
	ErrInternalServerError = NewErrorResponseForCode(http.StatusInternalServerError)
	ErrGatewayTimeout      = NewErrorResponseForCode(http.StatusGatewayTimeout)
	ErrBadGateway          = NewErrorResponseForCode(http.StatusBadGateway)
	ErrServiceUnavailable  = NewErrorResponseForCode(http.StatusServiceUnavailable)
	ErrTooManyRequests     = NewErrorResponseForCode(http.StatusTooManyRequests)
)

type ErrorRetryAfter struct {
	Err        error
	RetryAfter time.Duration
}

// NewErrorWithRetryAfter wraps any error in RetryAfter hint that
// gets passed to HTTP clients in Retry-After HTTP header.
func NewErrorRetryAfter(err error, retryAfter time.Duration) *ErrorRetryAfter {
	if err == nil {
		err = ErrServiceUnavailable
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &ErrorRetryAfter{
		RetryAfter: retryAfter,
		Err:        err,
	}
}

func (e *ErrorRetryAfter) Error() string {
	var text string
	if e.Err != nil {
		text = e.Err.Error()
	}
	if e.RetryAfter != 0 {
		text += fmt.Sprintf(", retry after %s", e.Humanized())
	}
	return text
}

func (e *ErrorRetryAfter) Unwrap() error {
	return e.Err
}

func (e *ErrorRetryAfter) Is(err error) bool {
	switch err.(type) {
	case *ErrorRetryAfter:
		return true
	default:
		return false
	}
}

func (e *ErrorRetryAfter) RoundSeconds() time.Duration {
	return e.RetryAfter.Round(time.Second)
}

func (e *ErrorRetryAfter) Humanized() string {
	return e.RoundSeconds().String()
}

// HTTPHeaderValue returns the Retry-After header value as a string, representing the number
// of seconds to wait before making a new request, rounded to the nearest second.
// This function follows the Retry-After header definition as specified in RFC 9110.
func (e *ErrorRetryAfter) HTTPHeaderValue() string {
	return strconv.Itoa(int(e.RoundSeconds().Seconds()))
}

// Custom type for collecting error details to be handled by `webError`. When an error
// of this type is returned to the gateway handler, the StatusCode will be used for
// the response status.
type ErrorResponse struct {
	StatusCode int
	Err        error
}

func NewErrorResponseForCode(statusCode int) *ErrorResponse {
	return NewErrorResponse(errors.New(http.StatusText(statusCode)), statusCode)
}

func NewErrorResponse(err error, statusCode int) *ErrorResponse {
	return &ErrorResponse{
		Err:        err,
		StatusCode: statusCode,
	}
}

func (e *ErrorResponse) Is(err error) bool {
	switch err.(type) {
	case *ErrorResponse:
		return true
	default:
		return false
	}
}

func (e *ErrorResponse) Error() string {
	var text string
	if e.Err != nil {
		text = e.Err.Error()
	}
	return text
}

func (e *ErrorResponse) Unwrap() error {
	return e.Err
}

func webError(w http.ResponseWriter, r *http.Request, c *Config, err error, defaultCode int) {
	code := defaultCode

	// Pass Retry-After hint to the client
	var era *ErrorRetryAfter
	if errors.As(err, &era) {
		if era.RetryAfter > 0 {
			w.Header().Set("Retry-After", era.HTTPHeaderValue())
			// Adjust defaultCode if needed
			if code != http.StatusTooManyRequests && code != http.StatusServiceUnavailable {
				code = http.StatusTooManyRequests
			}
		}
		err = era.Unwrap()
	}

	// Handle status code
	switch {
	case errors.Is(err, &cid.ErrInvalidCid{}):
		code = http.StatusBadRequest
	case isErrNotFound(err):
		code = http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	}

	// Handle explicit code in ErrorResponse
	var gwErr *ErrorResponse
	if errors.As(err, &gwErr) {
		code = gwErr.StatusCode
	}

	acceptsHTML := strings.Contains(r.Header.Get("Accept"), "text/html")
	if acceptsHTML {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(code)
		_ = assets.ErrorTemplate.Execute(w, assets.ErrorTemplateData{
			GlobalData: assets.GlobalData{
				Menu: c.Menu,
			},
			StatusCode: code,
			StatusText: http.StatusText(code),
			Error:      err.Error(),
		})
	} else {
		http.Error(w, err.Error(), code)
	}
}

func isErrNotFound(err error) bool {
	if ipld.IsNotFound(err) {
		return true
	}

	// Checks if err is of a type that does not implement the .Is interface and
	// cannot be directly compared to. Therefore, errors.Is cannot be used.
	for {
		_, ok := err.(resolver.ErrNoLink)
		if ok {
			return true
		}

		_, ok = err.(datamodel.ErrWrongKind)
		if ok {
			return true
		}

		_, ok = err.(datamodel.ErrNotExists)
		if ok {
			return true
		}

		err = errors.Unwrap(err)
		if err == nil {
			return false
		}
	}
}

func (i *handler) webRequestError(w http.ResponseWriter, r *http.Request, err *ErrorResponse) {
	i.webError(w, r, err.Err, err.StatusCode)
}
