package eligibility

import (
	"strings"
)

// Recognised keys of the request's free-form context object. Until the service catalogue
// of I3 can map a serviceDefinitionId onto an entitlement definition, the caller says
// which entitlement a line should be measured against; everything else in the context is
// ignored and never stored.
//
//	{"domain": "HEALTH",                    // SENSITIVE access audit instead of PERSONAL
//	 "entitlementCode": "DENTAL",           // request level: applies to every line
//	 "entitlementCodes": ["DENTAL", null]}  // per line, positional, wins over the above
const (
	contextKeyDomain           = "domain"
	contextKeyEntitlementCode  = "entitlementCode"
	contextKeyEntitlementCodes = "entitlementCodes"

	// DomainHealth marks a check made in a clinical context; its access audit row is
	// classified HEALTH rather than PERSONAL.
	DomainHealth = "HEALTH"
)

// contextHints is the recognised part of the request context.
type contextHints struct {
	domain string
	code   string
	codes  []string
}

// parseContext reads the hints out of the free-form context object. Unknown keys and
// values of the wrong type are ignored: the context is a hint, never a command.
func parseContext(raw map[string]any) contextHints {
	out := contextHints{}
	if raw == nil {
		return out
	}
	if s, ok := raw[contextKeyDomain].(string); ok {
		out.domain = strings.ToUpper(strings.TrimSpace(s))
	}
	if s, ok := raw[contextKeyEntitlementCode].(string); ok {
		out.code = strings.TrimSpace(s)
	}
	if list, ok := raw[contextKeyEntitlementCodes].([]any); ok {
		out.codes = make([]string, len(list))
		for i, v := range list {
			if s, ok := v.(string); ok {
				out.codes[i] = strings.TrimSpace(s)
			}
		}
	}
	return out
}

// codeFor is the entitlement code of one requested line: the positional hint when it is
// present and not empty, otherwise the request-level one, otherwise none.
func (h contextHints) codeFor(index int) string {
	if index >= 0 && index < len(h.codes) && h.codes[index] != "" {
		return h.codes[index]
	}
	return h.code
}

// health reports whether the check was made in a clinical context.
func (h contextHints) health() bool { return h.domain == DomainHealth }

// empty reports whether nothing was recognised, so the snapshot can omit the object.
func (h contextHints) empty() bool {
	return h.domain == "" && h.code == "" && len(h.codes) == 0
}

// sanitized is the context as it is stored and returned: recognised codes only.
func (h contextHints) sanitized() map[string]any {
	if h.empty() {
		return nil
	}
	out := map[string]any{}
	if h.domain != "" {
		out[contextKeyDomain] = h.domain
	}
	if h.code != "" {
		out[contextKeyEntitlementCode] = h.code
	}
	if len(h.codes) > 0 {
		codes := make([]any, len(h.codes))
		for i, c := range h.codes {
			if c == "" {
				codes[i] = nil
				continue
			}
			codes[i] = c
		}
		out[contextKeyEntitlementCodes] = codes
	}
	return out
}

// canonical is the hashed form: always an object, so a request without a context and a
// request with an unrecognised one hash the same way.
func (h contextHints) canonical() any {
	out := h.sanitized()
	if out == nil {
		return map[string]any{}
	}
	return out
}
