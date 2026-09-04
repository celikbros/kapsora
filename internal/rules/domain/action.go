package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/celikbros/kapsora/internal/rules/engine"
)

// Action payload field kinds. A payload is small and closed on purpose: an action is what
// a rule asks the caller to do, and a caller that has to guess at the shape of the request
// is a caller that will guess wrong.
type payloadKind int

const (
	// kindCode is an upper-case identifier: a document type, a queue, a limit.
	kindCode payloadKind = iota
	// kindDecimal is an exact decimal string; never a JSON number.
	kindDecimal
	// kindEnum is one of a closed list.
	kindEnum
	// kindText is free text shown to a reviewer, bounded in length.
	kindText
	// kindBool is a flag.
	kindBool
)

type payloadField struct {
	name     string
	kind     payloadKind
	required bool
	values   []string
}

// actionSpec is the typed payload of one action type. anyOf names fields of which exactly
// one must be present, which is how PARTIAL_APPROVE takes either a percent or an amount
// but never both.
type actionSpec struct {
	fields []payloadField
	anyOf  []string
}

// adjustPriceMethods is the closed list of ways ADJUST_PRICE moves a price.
var adjustPriceMethods = []string{"FIXED", "PERCENT", "DELTA"}

// limitPeriods is the closed list SET_LIMIT counts over; it mirrors the quota period type
// of the contract module, because "per year" has to mean the same thing in both.
var limitPeriods = []string{"DAY", "WEEK", "MONTH", "YEAR", "CONTRACT"}

// actionSpecs is the whole authoring contract of the closed action list (WP-I3-04 2.3).
// A payload is validated on write so a malformed action is rejected by the author's own
// screen and not at three in the morning by the caller that has to perform it.
var actionSpecs = map[string]actionSpec{
	engine.ActionApprove: {},
	engine.ActionReject: {fields: []payloadField{
		{name: "reasonCode", kind: kindCode},
		{name: "message", kind: kindText},
	}},
	engine.ActionWarn: {fields: []payloadField{
		{name: "messageCode", kind: kindCode},
		{name: "message", kind: kindText},
	}},
	engine.ActionRequireDocument: {fields: []payloadField{
		{name: "documentTypeCode", kind: kindCode, required: true},
		{name: "mandatory", kind: kindBool},
	}},
	engine.ActionRequirePreauth: {fields: []payloadField{
		{name: "authorizationTypeCode", kind: kindCode},
	}},
	engine.ActionRequireMedicalReview: {fields: []payloadField{
		{name: "queueCode", kind: kindCode},
	}},
	engine.ActionRequireFinancialReview: {fields: []payloadField{
		{name: "queueCode", kind: kindCode},
	}},
	engine.ActionPartialApprove: {
		fields: []payloadField{
			{name: "percent", kind: kindDecimal},
			{name: "amount", kind: kindDecimal},
		},
		anyOf: []string{"percent", "amount"},
	},
	engine.ActionReserveEntitlement: {fields: []payloadField{
		{name: "quantity", kind: kindDecimal, required: true},
		{name: "benefitCode", kind: kindCode},
	}},
	engine.ActionAdjustPrice: {fields: []payloadField{
		{name: "method", kind: kindEnum, required: true, values: adjustPriceMethods},
		{name: "value", kind: kindDecimal, required: true},
	}},
	engine.ActionSetLimit: {fields: []payloadField{
		{name: "amount", kind: kindDecimal, required: true},
		{name: "limitCode", kind: kindCode},
		{name: "periodType", kind: kindEnum, values: limitPeriods},
	}},
}

// ActionTypes is the closed list, sorted, for an error message and for the OpenAPI enum
// to be checked against.
func ActionTypes() []string {
	out := make([]string, 0, len(actionSpecs))
	for name := range actionSpecs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// maxPayloadTextLength bounds a free-text payload field; anything longer belongs in a
// translation file rather than in a rule.
const maxPayloadTextLength = 500

// validateActions checks an ordered action list against the closed list and the typed
// payload of each action type. An unknown key is refused rather than ignored: a payload
// with a mistyped field name would otherwise be silently dropped by the caller.
func validateActions(ve *ValidationError, path string, actions []ActionInput) {
	if len(actions) > MaxActionsPerRule {
		ve.Add(path, "LENGTH", "en fazla 20 aksiyon")
		return
	}
	for i, a := range actions {
		field := fmt.Sprintf("%s[%d]", path, i)
		spec, known := actionSpecs[a.Type]
		if !known {
			ve.Add(field+".type", "ENUM", "geçerli değerler: "+strings.Join(ActionTypes(), ", "))
			continue
		}
		validatePayload(ve, field+".payload", spec, a.Payload)
	}
}

// validateExpectedActions checks a test case's expectation rather than an authored
// action. The type must still be one of the closed list and a supplied payload field must
// still be well formed, but a required field may be left out: a case that asserts "this
// asks for a document" should not have to repeat every field of the request to say so.
func validateExpectedActions(ve *ValidationError, path string, actions []ActionInput) {
	if len(actions) > MaxExplanations {
		ve.Add(path, "LENGTH", "en fazla 100 aksiyon")
		return
	}
	for i, a := range actions {
		field := fmt.Sprintf("%s[%d]", path, i)
		spec, known := actionSpecs[a.Type]
		if !known {
			ve.Add(field+".type", "ENUM", "geçerli değerler: "+strings.Join(ActionTypes(), ", "))
			continue
		}
		byName := make(map[string]payloadField, len(spec.fields))
		for _, f := range spec.fields {
			byName[f.name] = f
		}
		for key, value := range a.Payload {
			f, ok := byName[key]
			if !ok {
				ve.Add(field+".payload."+key, "UNKNOWN_FIELD", "bu aksiyon böyle bir alan taşımaz")
				continue
			}
			validatePayloadValue(ve, field+".payload."+key, f, value)
		}
	}
}

func validatePayload(ve *ValidationError, path string, spec actionSpec, payload map[string]any) {
	byName := make(map[string]payloadField, len(spec.fields))
	for _, f := range spec.fields {
		byName[f.name] = f
	}
	for key, value := range payload {
		f, known := byName[key]
		if !known {
			ve.Add(path+"."+key, "UNKNOWN_FIELD", "bu aksiyon böyle bir alan taşımaz")
			continue
		}
		validatePayloadValue(ve, path+"."+key, f, value)
	}
	for _, f := range spec.fields {
		if f.required && !present(payload, f.name) {
			ve.Add(path+"."+f.name, "REQUIRED", "bu aksiyon için zorunlu")
		}
	}
	if len(spec.anyOf) > 0 {
		count := 0
		for _, name := range spec.anyOf {
			if present(payload, name) {
				count++
			}
		}
		if count != 1 {
			ve.Add(path, "REQUIRED", "yalnız biri verilmeli: "+strings.Join(spec.anyOf, ", "))
		}
	}
}

func validatePayloadValue(ve *ValidationError, path string, f payloadField, value any) {
	switch f.kind {
	case kindBool:
		if _, ok := value.(bool); !ok {
			ve.Add(path, "TYPE", "true veya false olmalı")
		}
	case kindCode:
		s, ok := value.(string)
		if !ok {
			ve.Add(path, "TYPE", "metin olmalı")
			return
		}
		if !payloadCodeRegex.MatchString(s) {
			ve.Add(path, "FORMAT", "büyük harfle başlayan bir kod olmalı")
		}
	case kindEnum:
		s, ok := value.(string)
		if !ok {
			ve.Add(path, "TYPE", "metin olmalı")
			return
		}
		requireOneOf(ve, path, s, f.values)
	case kindDecimal:
		s, ok := value.(string)
		if !ok {
			// A JSON number would arrive as a float64 and lose the exact value on the way,
			// so a decimal is text here and rejecting a number is the point.
			ve.Add(path, "TYPE", "tam ondalık bir metin olmalı, JSON sayısı değil")
			return
		}
		if !Decimal(s) {
			ve.Add(path, "FORMAT", "geçerli bir ondalık olmalı")
		}
	case kindText:
		s, ok := value.(string)
		if !ok {
			ve.Add(path, "TYPE", "metin olmalı")
			return
		}
		if len([]rune(s)) > maxPayloadTextLength {
			ve.Add(path, "LENGTH", "en fazla 500 karakter")
		}
	}
}

func present(payload map[string]any, name string) bool {
	v, ok := payload[name]
	return ok && v != nil
}

// EngineActions maps authored actions onto the evaluator's own type, which is the only
// place the two representations meet.
func EngineActions(actions []ActionInput) []engine.Action {
	out := make([]engine.Action, 0, len(actions))
	for _, a := range actions {
		out = append(out, engine.Action{Type: a.Type, Payload: a.Payload})
	}
	return out
}
