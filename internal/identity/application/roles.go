package application

import "github.com/celikbros/kapsora/internal/identity"

// RoleTemplate is a system role copied into every tenant at provisioning (v1.2 section 6.2).
// This table is the single source; a test checks every code against iam.permission.
type RoleTemplate struct {
	Code        string
	Name        string
	Description string
	// Scope is the grant scope the role is normally issued with: TENANT for back-office
	// roles, ORGANIZATION for provider-side roles bound to one provider organization.
	Scope       string
	Permissions []string
}

// Grant scope types (iam.access_grant.scope_type).
const (
	ScopeTenant           = "TENANT"
	ScopeOrganization     = "ORGANIZATION"
	ScopeProgram          = "PROGRAM"
	ScopeProviderLocation = "PROVIDER_LOCATION"
	ScopeWorkQueue        = "WORK_QUEUE"
	// ScopePerson binds a member account to the one person it acts for (migration
	// 000039). It is the identity package's constant under the name this file uses for
	// the others, so the grant writer and the context resolver compare the same string.
	ScopePerson = identity.ScopePerson
)

// RoleTemplates returns the templates in a stable order.
func RoleTemplates() []RoleTemplate {
	return []RoleTemplate{
		{Code: "TENANT_ADMIN", Name: "Kurum Yöneticisi", Scope: ScopeTenant,
			Description: "Kullanıcı, rol ve kurum ayarlarını yönetir; klinik veriye otomatik yetkisi yoktur.",
			Permissions: []string{"identity.user.read", "identity.user.manage", "identity.role.manage", "identity.access_review",
				"organization.read", "organization.manage", "program.read", "catalog.read", "provider.read", "contract.read",
				"rule.read", "report.read", "audit.read", "notification.manage", "notification.read",
				"integration.manage",
				"worklist.reassign", "workflow.queue.manage", "workflow.policy.manage",
				"document.legal_hold.manage"}},
		{Code: "PROGRAM_MANAGER", Name: "Program ve Fayda Yöneticisi", Scope: ScopeTenant,
			Description: "Program, plan taslağı, hak sahibi ve enrollment yönetimi.",
			Permissions: []string{"organization.read", "member.read", "member.manage", "member.relationship.manage", "membership.manage",
				"member.contact.read", "member.contact.manage",
				"enrollment.manage", "eligibility.check", "program.read", "program.manage", "plan.manage",
				"entitlement.read", "entitlement.mapping.manage",
				"catalog.read", "catalog.manage", "pricing.quote", "authorization.manage", "fulfilment.record",
				"voucher.redeem", "claim.read", "report.read", "worklist.read", "worklist.claim",
				"accommodation.property.read",
				"notification.read"}},
		{Code: "PLAN_PUBLISHER", Name: "Plan Onaylayıcı", Scope: ScopeTenant,
			Description: "Plan sürümü yayınlar ve hak düzeltmelerini onaylar (checker).",
			Permissions: []string{"program.read", "plan.publish", "entitlement.read", "entitlement.adjust"}},
		{Code: "CONTRACT_MANAGER", Name: "Sözleşme Yöneticisi", Scope: ScopeTenant,
			Description: "Sağlayıcı, lokasyon, uygulayıcı ve sözleşme taslağı yönetimi.",
			Permissions: []string{"organization.read", "provider.read", "provider.manage", "provider.practitioner.manage",
				"contract.read", "contract.manage", "contract.lodging_terms.manage", "catalog.read", "pricing.quote"}},
		{Code: "CONTRACT_PUBLISHER", Name: "Sözleşme Onaylayıcı", Scope: ScopeTenant,
			Description: "Sözleşme sürümü yayınlar (checker).",
			Permissions: []string{"contract.read", "contract.publish"}},
		{Code: "RULE_AUTHOR", Name: "Kural Yazarı", Scope: ScopeTenant,
			Description: "Kural taslağı ve test senaryosu yazar; kendi kuralını yayınlayamaz.",
			Permissions: []string{"rule.read", "rule.draft", "catalog.read", "program.read"}},
		{Code: "RULE_APPROVER", Name: "Kural Onaylayıcı", Scope: ScopeTenant,
			Description: "Kural sürümü yayınlar (checker).",
			Permissions: []string{"rule.read", "rule.publish"}},
		{Code: "MEDICAL_REVIEWER", Name: "Tıbbi Değerlendirici", Scope: ScopeTenant,
			Description: "Sağlık ön onayı, rapor ve klinik claim değerlendirmesi; settlement yetkisi yok.",
			Permissions: []string{"member.read", "service_request.read", "service_request.review", "authorization.manage",
				"health.case.read", "health.clinical.read", "health.sensitive.read",
				"health.medical_report.review", "claim.read",
				"claim.medical.review", "document.read", "document.link", "worklist.read", "worklist.claim"}},
		{Code: "FINANCIAL_REVIEWER", Name: "Mali Değerlendirici", Scope: ScopeTenant,
			Description: "Fiyat, fatura, kesinti, icmal, e-Belge eşleştirme ve mutabakat; klinik belge görmez.",
			Permissions: []string{"member.read", "service_request.read", "claim.read", "claim.financial.review", "invoice.read",
				"invoice.manage", "batch.review", "settlement.read", "fiscal.edocument.read", "fiscal.edocument.match",
				"accounting.posting.read", "document.read", "document.link", "pricing.quote", "report.read",
				"worklist.read", "worklist.claim"}},
		{Code: "PAYER_APPROVER", Name: "Ödeyici Onaylayıcı", Scope: ScopeTenant,
			Description: "Eşik bazlı ikinci onay: settlement, GİB yanıtı, muhasebe gönderimi.",
			Permissions: []string{"settlement.read", "settlement.approve", "fiscal.response.send", "accounting.posting.send",
				"accounting.reconcile", "report.read", "worklist.read", "worklist.claim"}},
		{Code: "AUDITOR", Name: "Denetçi", Scope: ScopeTenant,
			Description: "Salt okunur rapor ve audit erişimi.",
			Permissions: []string{"report.read", "audit.read", "security.audit.read", "entitlement.read",
				"notification.read"}},
		{Code: "PROVIDER_ADMIN", Name: "Sağlayıcı Yöneticisi", Scope: ScopeOrganization,
			Description: "Kendi kurumunun kullanıcı, lokasyon ve uygulayıcılarını yönetir.",
			Permissions: []string{"identity.user.read", "identity.user.manage", "provider.read", "provider.manage",
				"provider.practitioner.manage"}},
		{Code: "PROVIDER_STAFF", Name: "Sağlayıcı Kayıt/Klinik", Scope: ScopeOrganization,
			Description: "Hak sorgusu, hizmet talebi, sağlık vakası, belge ve hizmet kaydı.",
			Permissions: []string{"member.read", "eligibility.check", "service_request.read", "service_request.create",
				"service_request.submit", "service_request.cancel", "fulfilment.record", "voucher.redeem",
				"health.case.read", "health.case.manage", "health.clinical.read", "health.medical_report.manage",
				"document.upload", "document.read", "document.link", "pricing.quote"}},
		{Code: "PROVIDER_BILLING", Name: "Sağlayıcı Faturalama", Scope: ScopeOrganization,
			Description: "Claim, dış fatura, icmal ve settlement takibi; klinik belgeye minimum erişim.",
			Permissions: []string{"claim.read", "claim.create", "claim.submit", "claim.cancel",
				"invoice.read", "invoice.manage", "batch.create",
				"batch.submit", "settlement.read", "fiscal.edocument.read", "document.read", "document.link"}},
		// accommodation.property.read is the grant migration 000040 adds, and it is held by
		// the four roles that have a reason to look at a hotel: the member who will stay in
		// it, the provider clerk who maintains it, the programme manager who negotiated it
		// and the sponsor's HR user who is asked about it. It is a read of a building, not
		// of a person, which is why it is NORMAL and why holding it alone lets nobody book,
		// hold or change anything.
		{Code: "PROVIDER_RESERVATION", Name: "Sağlayıcı Rezervasyon", Scope: ScopeOrganization,
			Description: "Konaklama kontenjanı, rezervasyon, check-in/out; sağlık verisine erişemez.",
			Permissions: []string{"accommodation.property.read", "accommodation.inventory.manage",
				"accommodation.booking.manage", "member.read", "eligibility.check"}},
		// The sponsor's own HR user. It exists so the acceptance criterion of WP-I5-01 has a
		// subject: this is the role that may see that a member has an open health case, and
		// may never see what the case is about. health.clinical.read is absent on purpose,
		// and a test asserts its absence — a permission quietly added here would defeat the
		// whole package without a single line of it changing.
		{Code: "SPONSOR_HR", Name: "Sponsor İK", Scope: ScopeTenant,
			Description: "Sponsor kurumun İK kullanıcısı: üye, talep ve hak durumu görür; klinik detaya asla erişemez.",
			Permissions: []string{"member.read", "service_request.read", "health.case.read",
				"claim.read", "entitlement.read", "report.read", "accommodation.property.read"}},
		{Code: "MEMBER", Name: "Hak Sahibi", Scope: ScopeTenant,
			Description: "Kendi hakları, başvuruları, rezervasyonları ve belgeleri.",
			Permissions: []string{"eligibility.check", "service_request.read", "service_request.create", "service_request.submit",
				"service_request.cancel", "accommodation.property.read", "accommodation.booking.create",
				"document.upload", "document.read", "entitlement.read"}},
	}
}

// RoleTemplateByCode looks a template up.
func RoleTemplateByCode(code string) (RoleTemplate, bool) {
	for _, t := range RoleTemplates() {
		if t.Code == code {
			return t, true
		}
	}
	return RoleTemplate{}, false
}
