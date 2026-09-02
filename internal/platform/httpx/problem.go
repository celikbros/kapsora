// Package httpx holds HTTP plumbing shared by every transport: RFC 9457 problem
// responses, request correlation and logging middleware.
package httpx

import (
	"encoding/json"
	"net/http"
)

// ProblemTypeBase prefixes machine-readable problem type URIs.
const ProblemTypeBase = "https://errors.kapsora.example/"

// Problem is the application/problem+json body defined in the OpenAPI contract.
type Problem struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Code     string       `json:"code"`
	TraceID  string       `json:"traceId"`
	Errors   []FieldError `json:"errors,omitempty"`
}

// FieldError describes one validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// WriteProblem serialises p, filling traceId and instance from the request when
// they are empty. Detail must already be safe to show to the caller.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.TraceID == "" {
		p.TraceID = RequestIDFrom(r.Context())
	}
	if p.Instance == "" && r != nil {
		p.Instance = r.URL.Path
	}
	if p.Type == "" {
		p.Type = ProblemTypeBase + "generic/" + http.StatusText(p.Status)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Common problems.

func problemNotFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, Problem{
		Type:   ProblemTypeBase + "generic/not-found",
		Title:  "Kaynak bulunamadı",
		Status: http.StatusNotFound,
		Code:   "RESOURCE_NOT_FOUND",
	})
}

func problemMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, Problem{
		Type:   ProblemTypeBase + "generic/method-not-allowed",
		Title:  "Yöntem desteklenmiyor",
		Status: http.StatusMethodNotAllowed,
		Code:   "METHOD_NOT_ALLOWED",
	})
}

// NotFoundHandler is the router fallback for unknown paths.
func NotFoundHandler() http.HandlerFunc { return problemNotFound }

// MethodNotAllowedHandler is the router fallback for known paths, wrong method.
func MethodNotAllowedHandler() http.HandlerFunc { return problemMethodNotAllowed }
