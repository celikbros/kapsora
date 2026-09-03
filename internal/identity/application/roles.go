package application

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
)

// RoleTemplates returns the templates in a stable order.
func RoleTemplates() []RoleTemplate {
	return []RoleTemplate{
		{Code: "TENANT_ADMIN", Name: "Kurum Yöneticisi", Scope: ScopeTenant,
			Description: "Kullanıcı, rol ve kurum ayarlarını yönetir; klinik veriye otomatik yetkisi yoktur.",
			Permissions: []string{"identity.user.read", "identity.user.manage", "identity.role.manage", "identity.access_review",
				"organization.read", "organization.manage", "program.read", "catalog.read", "provider.read", "contract.read",
				"rule.read", "report.read", "audit.read", "notification.manage", "integration.manage"}},
		{Code: "PROGRAM_MANAGER", Name: "Program ve Fayda Yöneticisi", Scope: ScopeTenant,
			Description: "Program, plan taslağı, hak sahibi ve enrollment yönetimi.",
			Permissions: []string{"organization.read", "member.read", "member.manage", "member.relationship.manage", "membership.manage",
				"enrollment.manage", "eligibility.check", "program.read", "program.manage", "plan.manage", "entitlement.read",
				"catalog.read", "catalog.manage", "report.read"}},
		{Code: "PLAN_PUBLISHER", Name: "Plan Onaylayıcı", Scope: ScopeTenant,
			Description: "Plan sürümü yayınlar ve hak düzeltmelerini onaylar (checker).",
			Permissions: []string{"program.read", "plan.publish", "entitlement.read", "entitlement.adjust"}},
		{Code: "CONTRACT_MANAGER", Name: "Sözleşme Yöneticisi", Scope: ScopeTenant,
			Description: "Sağlayıcı, lokasyon, uygulayıcı ve sözleşme taslağı yönetimi.",
			Permissions: []string{"organization.read", "provider.read", "provider.manage", "provider.practitioner.manage",
				"contract.read", "contract.manage", "catalog.read"}},
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
			Permissions: []string{"member.read", "service_request.read", "service_request.review", "health.case.read",
				"health.clinical.read", "health.medical_report.review", "claim.read", "claim.medical.review", "document.read"}},
		{Code: "FINANCIAL_REVIEWER", Name: "Mali Değerlendirici", Scope: ScopeTenant,
			Description: "Fiyat, fatura, kesinti, icmal, e-Belge eşleştirme ve mutabakat; klinik belge görmez.",
			Permissions: []string{"member.read", "service_request.read", "claim.read", "claim.financial.review", "invoice.read",
				"invoice.manage", "batch.review", "settlement.read", "fiscal.edocument.read", "fiscal.edocument.match",
				"accounting.posting.read", "document.read", "report.read"}},
		{Code: "PAYER_APPROVER", Name: "Ödeyici Onaylayıcı", Scope: ScopeTenant,
			Description: "Eşik bazlı ikinci onay: settlement, GİB yanıtı, muhasebe gönderimi.",
			Permissions: []string{"settlement.read", "settlement.approve", "fiscal.response.send", "accounting.posting.send",
				"accounting.reconcile", "report.read"}},
		{Code: "AUDITOR", Name: "Denetçi", Scope: ScopeTenant,
			Description: "Salt okunur rapor ve audit erişimi.",
			Permissions: []string{"report.read", "audit.read", "security.audit.read", "entitlement.read"}},
		{Code: "PROVIDER_ADMIN", Name: "Sağlayıcı Yöneticisi", Scope: ScopeOrganization,
			Description: "Kendi kurumunun kullanıcı, lokasyon ve uygulayıcılarını yönetir.",
			Permissions: []string{"identity.user.read", "identity.user.manage", "provider.read", "provider.manage",
				"provider.practitioner.manage"}},
		{Code: "PROVIDER_STAFF", Name: "Sağlayıcı Kayıt/Klinik", Scope: ScopeOrganization,
			Description: "Hak sorgusu, hizmet talebi, sağlık vakası, belge ve hizmet kaydı.",
			Permissions: []string{"member.read", "eligibility.check", "service_request.read", "service_request.create",
				"service_request.submit", "service_request.cancel", "health.case.read", "health.case.manage",
				"health.clinical.read", "health.medical_report.manage", "document.upload", "document.read"}},
		{Code: "PROVIDER_BILLING", Name: "Sağlayıcı Faturalama", Scope: ScopeOrganization,
			Description: "Claim, dış fatura, icmal ve settlement takibi; klinik belgeye minimum erişim.",
			Permissions: []string{"claim.read", "claim.create", "claim.submit", "invoice.read", "invoice.manage", "batch.create",
				"batch.submit", "settlement.read", "fiscal.edocument.read", "document.read"}},
		{Code: "PROVIDER_RESERVATION", Name: "Sağlayıcı Rezervasyon", Scope: ScopeOrganization,
			Description: "Konaklama kontenjanı, rezervasyon, check-in/out; sağlık verisine erişemez.",
			Permissions: []string{"accommodation.inventory.manage", "accommodation.booking.manage", "member.read", "eligibility.check"}},
		{Code: "MEMBER", Name: "Hak Sahibi", Scope: ScopeTenant,
			Description: "Kendi hakları, başvuruları, rezervasyonları ve belgeleri.",
			Permissions: []string{"eligibility.check", "service_request.read", "service_request.create", "service_request.submit",
				"service_request.cancel", "accommodation.booking.create", "document.upload", "document.read", "entitlement.read"}},
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
