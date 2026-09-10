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
				"voucher.redeem", "claim.read", "report.read", "report.export",
				"worklist.read", "worklist.claim",
				"accommodation.property.read", "accommodation.waitlist.manage",
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
				"invoice.manage", "batch.review", "settlement.read", "settlement.record_payment",
				"fiscal.edocument.read", "fiscal.edocument.match",
				"accounting.posting.read", "document.read", "document.link", "pricing.quote", "report.read",
				// The two halves of migration 000047. The mali degerlendirici is the person who
				// takes the numbers out of the building -- the cari ekstre a provider disputes, the
				// settlement list a bank reconciliation is checked against -- so it holds
				// report.export. It also holds report.export.sensitive, because deciding a claim on
				// its merits is exactly the work that needs the line descriptions, and a reviewer
				// who may read them one claim at a time and may not export a month of them would
				// simply copy them out by hand.
				"report.export", "report.export.sensitive",
				"worklist.read", "worklist.claim"}},
		{Code: "PAYER_APPROVER", Name: "Ödeyici Onaylayıcı", Scope: ScopeTenant,
			Description: "Eşik bazlı ikinci onay: settlement, GİB yanıtı, muhasebe gönderimi.",
			// settlement.record_payment is migration 000046's, and both finance roles hold it:
			// entering the bank's reference against an approved settlement is an ordinary
			// clerk's task rather than the second pair of eyes. It is a separate grant from
			// settlement.approve so that a tenant that wants the approver never to touch the
			// payment file can arrange that, which is impossible while the two are one
			// permission (WP-I7-04 §2.3).
			Permissions: []string{"settlement.read", "settlement.approve", "settlement.record_payment",
				"fiscal.response.send", "accounting.posting.send",
				// report.export and not report.export.sensitive: the approver releases money and
				// reads the settlement and reconciliation figures that justify it, and has no
				// reason to hold a spreadsheet of what members were treated for.
				"accounting.reconcile", "report.read", "report.export",
				"worklist.read", "worklist.claim"}},
		{Code: "AUDITOR", Name: "Denetçi", Scope: ScopeTenant,
			Description: "Salt okunur rapor ve audit erişimi.",
			// report.export.sensitive without report.export would be a grant nobody could use, so
			// the auditor holds both: an access review that could not take its own evidence out of
			// the system would be an audit conducted by screenshot.
			Permissions: []string{"report.read", "report.export", "report.export.sensitive",
				"audit.read", "security.audit.read", "entitlement.read",
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
				"batch.submit", "settlement.read", "fiscal.edocument.read", "document.read",
				// WP-I7-05 §2.2: the provider reads its own cari ekstre -- what it billed, what the
				// payer decided, what was settled and what is still open. It is `report.read` and
				// deliberately not `report.export`: the figures are the provider's own to look at,
				// and a file leaving the payer's tenant is the payer's decision.
				"report.read",
				"document.link"}},
		// accommodation.property.read is the grant migration 000040 adds, and it is held by
		// the four roles that have a reason to look at a hotel: the member who will stay in
		// it, the provider clerk who maintains it, the programme manager who negotiated it
		// and the sponsor's HR user who is asked about it. It is a read of a building, not
		// of a person, which is why it is NORMAL and why holding it alone lets nobody book,
		// hold or change anything.
		{Code: "PROVIDER_RESERVATION", Name: "Sağlayıcı Rezervasyon", Scope: ScopeOrganization,
			Description: "Konaklama kontenjanı, rezervasyon, check-in/out; sağlık verisine erişemez.",
			// document.read and document.upload: a no-show is reported with evidence, and the
			// desk that reports it is the one that has the evidence (WP-I6-03 §2.4).
			Permissions: []string{"accommodation.property.read", "accommodation.inventory.manage",
				"accommodation.booking.manage", "accommodation.waitlist.manage",
				"member.read", "eligibility.check", "document.read", "document.upload"}},
		// The sponsor's own HR user. It exists so the acceptance criterion of WP-I5-01 has a
		// subject: this is the role that may see that a member has an open health case, and
		// may never see what the case is about. health.clinical.read is absent on purpose,
		// and a test asserts its absence — a permission quietly added here would defeat the
		// whole package without a single line of it changing.
		// invoice.read is WP-I7-02's §2.3: the sponsor's HR user reads the headers and the
		// totals of the invoices raised against its members' claims, and never a claim line
		// description -- which is the same projection rule, applied to the invoice's claim
		// links. It reads money, not medicine, which is why it sits here beside claim.read
		// and why health.clinical.read still does not.
		{Code: "SPONSOR_HR", Name: "Sponsor İK", Scope: ScopeTenant,
			Description: "Sponsor kurumun İK kullanıcısı: üye, talep ve hak durumu görür; klinik detaya asla erişemez.",
			Permissions: []string{"member.read", "service_request.read", "health.case.read",
				"claim.read", "invoice.read", "entitlement.read", "report.read",
				"accommodation.property.read"}},
		{Code: "MEMBER", Name: "Hak Sahibi", Scope: ScopeTenant,
			Description: "Kendi hakları, başvuruları, rezervasyonları ve belgeleri.",
			Permissions: []string{"eligibility.check", "service_request.read", "service_request.create", "service_request.submit",
				"service_request.cancel", "accommodation.property.read", "accommodation.booking.create",
				"document.upload", "document.read", "document.link", "entitlement.read",
				// WP-I7-06: the reimbursement form names the service and the provider the member paid,
				// and links the receipt to the member's own request. All three read only what the
				// network already publishes; the PERSON grant on the membership bounds the rest.
				"catalog.read", "provider.read"}},
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
