package pricinghttp_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/pricing/application"
	pricingpg "github.com/celikbros/kapsora/internal/pricing/infrastructure/postgres"
	pricinghttp "github.com/celikbros/kapsora/internal/pricing/transport/http"
)

// The permission each request carries comes from a test header, so one server can show
// both the granted and the refused path.
const permsHeader = "X-Test-Permissions"

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	denied  *denyRecorder

	tenant     uuid.UUID
	actor      uuid.UUID
	provider   uuid.UUID
	person     uuid.UUID
	definition uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: pricingpg.New(), Audit: auditpg.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{h: h, denied: &denyRecorder{}}
	s.seed(t)

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := pricinghttp.NewHandler(svc, s.denied, logger)

	// Stand-in for RequireTenantContext: the permissions come from a test header.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: s.actor},
				Permissions: map[string]struct{}{},
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(fakeContext)
		api.Route("/pricing", handler.Routes)
	})
	s.handler = r
	return s
}

// seed is the smallest world a quote can be made in: a provider, a person, a catalogue
// definition and a published contract version pricing it at 500 TRY.
func (s *server) seed(t *testing.T) {
	t.Helper()
	h := s.h
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	s.tenant = h.CreateTenant("HTTP_PRICING")
	s.actor = h.CreateActor("pricing-http-clerk", "Pricing Clerk")
	payer := h.CreateTenantOrganization(s.tenant, "Pricing HTTP Payer", "PAYER")
	providerOrg := h.CreateTenantOrganization(s.tenant, "Pricing HTTP Provider", "PROVIDER")

	scan(&s.provider, "provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'CLINIC', 'ACTIVE') RETURNING id`, s.tenant, providerOrg)
	scan(&s.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Asli', 'Yildizhan', 'asli yildizhan') RETURNING id`, s.tenant)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant)
	scan(&s.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi seansı', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, category)

	var contractID, versionID, priceList uuid.UUID
	scan(&contractID, "contract", `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'HEALTH_2026', 'Sağlık 2026', $2, $3, 'HEALTH', 'ACTIVE') RETURNING id`,
		s.tenant, payer, s.provider)
	scan(&versionID, "contract version", `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, valid_to, currency_code,
		                                       configuration_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', '2027-01-01', 'TRY', 'deadbeef',
		        clock_timestamp(), $3) RETURNING id`, s.tenant, contractID, s.actor)
	scan(&priceList, "price list", `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'STANDART', 'Standart liste') RETURNING id`, s.tenant, versionID)
	h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, valid_from)
		VALUES ($1, $2, $3, 'SESSION', 'FIXED', 500, '2026-01-01')`,
		s.tenant, priceList, s.definition)
}

type response struct {
	code int
	body map[string]any
}

func (s *server) do(t *testing.T, method, path, perms, body string, headers map[string]string) response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(permsHeader, perms)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	out := response{code: rec.Code}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
			t.Fatalf("%s %s: body is not JSON: %s", method, path, rec.Body.String())
		}
	}
	return out
}

func (s *server) quoteBody() string {
	return `{"personId":"` + s.person.String() + `",` +
		`"providerProfileId":"` + s.provider.String() + `",` +
		`"serviceDate":"2026-06-15",` +
		`"items":[{"serviceDefinitionId":"` + s.definition.String() + `","quantity":"1"}]}`
}

// TestCreatePriceQuoteAnswersWithTheWholeArithmetic.
func TestCreatePriceQuoteAnswersWithTheWholeArithmetic(t *testing.T) {
	s := newServer(t)
	res := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, s.quoteBody(), nil)
	if res.code != http.StatusOK {
		t.Fatalf("status = %d, body %v", res.code, res.body)
	}
	if res.body["contractAmount"] != "500" {
		t.Fatalf("contractAmount = %v, want the exact decimal string 500", res.body["contractAmount"])
	}
	if res.body["currencyCode"] != "TRY" {
		t.Fatalf("currencyCode = %v", res.body["currencyCode"])
	}
	// The disclaimer is what stops somebody at a counter reading a quote as an approval,
	// so it is part of the contract rather than a nicety of the UI.
	disclaimer, _ := res.body["disclaimer"].(string)
	if disclaimer == "" {
		t.Fatal("the response carries no disclaimer")
	}
	if res.body["expired"] != false {
		t.Fatalf("expired = %v, want false", res.body["expired"])
	}
	if _, ok := res.body["ruleSetVersionIds"].([]any); !ok {
		t.Fatalf("ruleSetVersionIds is not a list: %v", res.body["ruleSetVersionIds"])
	}
	items, _ := res.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", res.body["items"])
	}
	line, _ := items[0].(map[string]any)
	if line["lineNo"] != float64(1) || line["memberAmount"] != "500" {
		t.Fatalf("line = %v", line)
	}
}

// TestGetPriceQuoteReadsItBack.
func TestGetPriceQuoteReadsItBack(t *testing.T) {
	s := newServer(t)
	created := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, s.quoteBody(), nil)
	if created.code != http.StatusOK {
		t.Fatalf("create: %d %v", created.code, created.body)
	}
	id, _ := created.body["id"].(string)

	read := s.do(t, http.MethodGet, "/api/v1/pricing/quotes/"+id, application.PermissionQuote, "", nil)
	if read.code != http.StatusOK {
		t.Fatalf("read: %d %v", read.code, read.body)
	}
	if read.body["id"] != id || read.body["contractAmount"] != created.body["contractAmount"] {
		t.Fatalf("read differs from what was given: %v", read.body)
	}

	missing := s.do(t, http.MethodGet, "/api/v1/pricing/quotes/"+uuid.New().String(),
		application.PermissionQuote, "", nil)
	if missing.code != http.StatusNotFound || missing.body["code"] != "PRICE_QUOTE_NOT_FOUND" {
		t.Fatalf("unknown quote: %d %v", missing.code, missing.body)
	}

	// A malformed id is indistinguishable from an unknown one, so the endpoint cannot be
	// used to tell "no such quote" from "not yours".
	malformed := s.do(t, http.MethodGet, "/api/v1/pricing/quotes/not-a-uuid",
		application.PermissionQuote, "", nil)
	if malformed.code != http.StatusNotFound {
		t.Fatalf("malformed id: %d %v", malformed.code, malformed.body)
	}
}

// TestPriceQuoteReplaysUnderTheIdempotencyKeyHeader.
func TestPriceQuoteReplaysUnderTheIdempotencyKeyHeader(t *testing.T) {
	s := newServer(t)
	headers := map[string]string{"Idempotency-Key": "pricing-http-key-000001"}

	first := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, s.quoteBody(), headers)
	second := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, s.quoteBody(), headers)
	if first.code != http.StatusOK || second.code != http.StatusOK {
		t.Fatalf("statuses = %d, %d", first.code, second.code)
	}
	if first.body["id"] != second.body["id"] {
		t.Fatalf("replay produced a second quote: %v then %v", first.body["id"], second.body["id"])
	}

	different := `{"personId":"` + s.person.String() + `",` +
		`"providerProfileId":"` + s.provider.String() + `","serviceDate":"2026-06-16",` +
		`"items":[{"serviceDefinitionId":"` + s.definition.String() + `","quantity":"1"}]}`
	reused := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, different, headers)
	if reused.code != http.StatusConflict || reused.body["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("reused key: %d %v", reused.code, reused.body)
	}
}

// TestPriceQuoteRefusesABadRequest.
func TestPriceQuoteRefusesABadRequest(t *testing.T) {
	s := newServer(t)

	unknownField := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote,
		`{"personId":"`+s.person.String()+`","surprise":1}`, nil)
	if unknownField.code != http.StatusBadRequest || unknownField.body["code"] != "INVALID_REQUEST_BODY" {
		t.Fatalf("unknown field: %d %v", unknownField.code, unknownField.body)
	}

	badQuantity := `{"personId":"` + s.person.String() + `",` +
		`"providerProfileId":"` + s.provider.String() + `","serviceDate":"2026-06-15",` +
		`"items":[{"serviceDefinitionId":"` + s.definition.String() + `","quantity":"1.0000001"}]}`
	res := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, badQuantity, nil)
	if res.code != http.StatusUnprocessableEntity || res.body["code"] != "VALIDATION_FAILED" {
		t.Fatalf("quantity beyond the column scale: %d %v", res.code, res.body)
	}

	unknownProvider := `{"personId":"` + s.person.String() + `",` +
		`"providerProfileId":"` + uuid.New().String() + `","serviceDate":"2026-06-15",` +
		`"items":[{"serviceDefinitionId":"` + s.definition.String() + `","quantity":"1"}]}`
	provider := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", application.PermissionQuote, unknownProvider, nil)
	if provider.code != http.StatusNotFound || provider.body["code"] != "PRICE_QUOTE_PROVIDER_NOT_FOUND" {
		t.Fatalf("unknown provider: %d %v", provider.code, provider.body)
	}
}

// TestPriceQuoteNeedsThePermission: both routes are guarded, and the refusal goes through
// the auditing denier rather than being written here.
func TestPriceQuoteNeedsThePermission(t *testing.T) {
	s := newServer(t)

	post := s.do(t, http.MethodPost, "/api/v1/pricing/quotes", "contract.read", s.quoteBody(), nil)
	if post.code != http.StatusForbidden {
		t.Fatalf("post without the permission: %d %v", post.code, post.body)
	}
	get := s.do(t, http.MethodGet, "/api/v1/pricing/quotes/"+uuid.New().String(), "", "", nil)
	if get.code != http.StatusForbidden {
		t.Fatalf("get without the permission: %d %v", get.code, get.body)
	}
	if len(s.denied.permissions) != 2 {
		t.Fatalf("denials audited = %d, want 2", len(s.denied.permissions))
	}
	for _, p := range s.denied.permissions {
		if p != application.PermissionQuote {
			t.Fatalf("denied permission = %s", p)
		}
	}
}
