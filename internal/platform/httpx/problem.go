// Package httpx holds HTTP plumbing shared by every transport: RFC 9457 problem
// responses, request correlation and logging middleware.
package httpx

import (
	"encoding/json"
	"net/http"
	"sort"
)

// ProblemTypeBase prefixes machine-readable problem type URIs.
const ProblemTypeBase = "https://errors.kapsora.example/"

// Problem is the application/problem+json body defined in the OpenAPI contract.
//
// Extensions are RFC 9457 extension members: named values that belong to one problem type
// and nowhere else, serialised flat beside the standard members rather than nested under a
// key of their own, which is what the RFC says an extension member is. They exist so a
// refusal can carry the one fact that makes it actionable — who holds the work item you
// tried to claim — without the loser having to make a second request to find out.
//
// A problem with no extensions serialises byte for byte as it did before they existed:
// MarshalJSON returns the ordinary encoding untouched. That is deliberate, because every
// problem the product emits today is one of those.
type Problem struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Code     string       `json:"code"`
	TraceID  string       `json:"traceId"`
	Errors   []FieldError `json:"errors,omitempty"`

	// Extensions is never itself a JSON member; see MarshalJSON.
	Extensions map[string]any `json:"-"`
}

// reservedProblemMembers are the standard members of RFC 9457 plus the ones this contract
// adds. An extension may not overwrite one: a problem whose `status` came from a caller's
// map would be a problem that lies about itself.
var reservedProblemMembers = map[string]bool{
	"type": true, "title": true, "status": true, "detail": true,
	"instance": true, "code": true, "traceId": true, "errors": true,
}

// MarshalJSON writes the standard members and then the extension members beside them, in
// a stable order. It appends to the encoding of the struct rather than rebuilding the
// object from a map, so the members that were there before extensions existed keep their
// order and their exact rendering.
func (p Problem) MarshalJSON() ([]byte, error) {
	type plain Problem
	body, err := json.Marshal(plain(p))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(p.Extensions))
	for name := range p.Extensions {
		if reservedProblemMembers[name] {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return body, nil
	}
	sort.Strings(names)
	out := make([]byte, 0, len(body)+len(names)*32)
	out = append(out, body[:len(body)-1]...)
	for _, name := range names {
		key, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(p.Extensions[name])
		if err != nil {
			return nil, err
		}
		out = append(out, ',')
		out = append(out, key...)
		out = append(out, ':')
		out = append(out, value...)
	}
	return append(out, '}'), nil
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

// The two refusals a member-side command has that no other caller does (WP-I6-04). They
// live here rather than in each transport because WP-I6-01..03 raise them from three more
// modules, and a member who reads "you are not bound to a person" from one screen and
// "permission denied" from the next would have no way to tell that both mean the same
// thing.
//
// Both are 403 rather than 404: the caller is authenticated and the resource exists; what
// is missing is the caller's standing to act for the person named.

// WritePersonScopeProblem answers a member naming somebody other than themselves. The
// detail says what happened without naming the person that was asked for, because echoing
// a person id back would let a caller test whether one exists.
func WritePersonScopeProblem(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, Problem{
		Type:   ProblemTypeBase + "identity/person-scope",
		Title:  "Yalnızca kendi adınıza işlem yapabilirsiniz",
		Status: http.StatusForbidden,
		Code:   "PERSON_SCOPE",
		Detail: "Bu hesap tek bir hak sahibi adına işlem yapar; istek başka bir kişiyi gösteriyor.",
	})
}

// WritePersonBindingMissingProblem answers an account that has not been bound to a person
// at all. It is deliberately a different code from PERSON_SCOPE: nobody has to grant this
// account anything, somebody has to finish binding it, and a member told "permission
// denied" would go looking for the wrong help.
func WritePersonBindingMissingProblem(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, Problem{
		Type:   ProblemTypeBase + "identity/person-binding-missing",
		Title:  "Hesabınız bir hak sahibiyle eşleştirilmemiş",
		Status: http.StatusForbidden,
		Code:   "PERSON_BINDING_MISSING",
		Detail: "Üye hesabınızın kayıt işlemi tamamlanmadığı için kendi bilgilerinizi göremiyoruz; kurumunuzun yetkilisine başvurun.",
	})
}

// WriteOwnFileProblem answers a reviewer deciding a file that belongs to the person they are.
// It names nothing about the file: the reviewer already knows whose it is, and the point of
// the answer is who should decide it instead.
func WriteOwnFileProblem(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, r, Problem{
		Type:   ProblemTypeBase + "identity/own-file-decision",
		Title:  "Bu dosya size ait",
		Status: http.StatusForbidden,
		Code:   "OWN_FILE_DECISION",
		Detail: "Kendi dosyanıza karar veremezsiniz; başka bir değerlendirici karar vermeli.",
	})
}
