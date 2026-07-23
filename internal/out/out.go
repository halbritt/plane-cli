// Package out owns plane-cli's output contract: the JSON envelope on stdout
// and the process exit-code table. Nothing else in the program writes to
// stdout.
//
// Success: {"ok":true,"data":...,"meta":{...}}
// Failure: {"ok":false,"error":{"code":...,"message":...,"http_status":...,"details":...}}
//
// Exit codes:
//
//	0 success
//	2 usage/config error
//	3 auth (HTTP 401/403)
//	4 not found (HTTP 404)
//	5 validation (other 4xx, incl. 409 conflict, ambiguous name resolution)
//	6 server/network (5xx, timeouts, connection errors)
//	7 rate-limited after retries exhausted (HTTP 429)
package out

import (
	"encoding/json"
	"fmt"
	"io"
)

const (
	ExitOK          = 0
	ExitUsage       = 2
	ExitAuth        = 3
	ExitNotFound    = 4
	ExitValidation  = 5
	ExitServer      = 6
	ExitRateLimited = 7
)

// Error codes carried in the envelope's error.code field.
const (
	CodeUsage       = "usage"
	CodeConfig      = "config"
	CodeAuth        = "auth"
	CodeNotFound    = "not_found"
	CodeValidation  = "validation"
	CodeConflict    = "conflict"
	CodeAmbiguous   = "ambiguous"
	CodeRateLimited = "rate_limited"
	CodeServer      = "server"
	CodeNetwork     = "network"
)

var exitByCode = map[string]int{
	CodeUsage:       ExitUsage,
	CodeConfig:      ExitUsage,
	CodeAuth:        ExitAuth,
	CodeNotFound:    ExitNotFound,
	CodeValidation:  ExitValidation,
	CodeConflict:    ExitValidation,
	CodeAmbiguous:   ExitValidation,
	CodeRateLimited: ExitRateLimited,
	CodeServer:      ExitServer,
	CodeNetwork:     ExitServer,
}

// ExitCode returns the process exit code for an envelope error code.
func ExitCode(code string) int {
	if c, ok := exitByCode[code]; ok {
		return c
	}
	return ExitServer
}

// CodeForHTTPStatus maps an HTTP response status to an envelope error code.
func CodeForHTTPStatus(status int) string {
	switch {
	case status == 401 || status == 403:
		return CodeAuth
	case status == 404:
		return CodeNotFound
	case status == 409:
		return CodeConflict
	case status == 429:
		return CodeRateLimited
	case status >= 400 && status < 500:
		return CodeValidation
	default:
		return CodeServer
	}
}

type Meta map[string]any

type ErrObj struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Details    any    `json:"details,omitempty"`
}

type Envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Meta  Meta            `json:"meta,omitempty"`
	Error *ErrObj         `json:"error,omitempty"`
}

// Success writes a success envelope to w and returns exit code 0.
// data may be nil (e.g. deletes), a json.RawMessage passed through verbatim,
// or any marshalable value.
func Success(w io.Writer, data any, meta Meta) int {
	env := Envelope{OK: true, Meta: meta}
	env.Data = marshalData(data)
	emit(w, env)
	return ExitOK
}

// Failure writes a failure envelope to w and returns the mapped exit code.
func Failure(w io.Writer, e ErrObj) int {
	emit(w, Envelope{OK: false, Error: &e})
	return ExitCode(e.Code)
}

func marshalData(data any) json.RawMessage {
	switch d := data.(type) {
	case nil:
		return json.RawMessage("null")
	case json.RawMessage:
		if len(d) == 0 {
			return json.RawMessage("null")
		}
		return d
	default:
		b, err := json.Marshal(d)
		if err != nil {
			// Marshaling CLI-constructed values cannot legitimately fail;
			// surface it rather than corrupt the stream.
			return json.RawMessage(fmt.Sprintf("%q", "marshal error: "+err.Error()))
		}
		return b
	}
}

func emit(w io.Writer, env Envelope) {
	b, err := json.Marshal(env)
	if err != nil {
		// Fall back to a minimal, hand-built envelope; stdout must stay JSON.
		fmt.Fprintf(w, `{"ok":false,"error":{"code":"server","message":%q}}%s`,
			"envelope marshal error: "+err.Error(), "\n")
		return
	}
	w.Write(append(b, '\n'))
}
