package contracthttp

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
)

// PutPriceLists implements putPriceLists; If-Match carries the ETag of the contract
// version, which the replacement moves on.
func (h *Handler) PutPriceLists(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplacePriceListsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.PriceListInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.PriceListInput{Code: row.Code, Name: row.Name, WeekdayMask: row.WeekdayMask}
		if row.Priority != nil {
			in.Priority = *row.Priority
		}
		if row.SeasonFrom != nil {
			from := row.SeasonFrom.Time
			in.SeasonFrom = &from
		}
		if row.SeasonTo != nil {
			to := row.SeasonTo.Time
			in.SeasonTo = &to
		}
		items = append(items, in)
	}

	result, err := h.svc.ReplacePriceLists(r.Context(), rc, versionID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, priceListListView(result.Items))
}

// ListPriceItems implements listPriceItems; the ETag is the one of the owning price list.
func (h *Handler) ListPriceItems(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	listID, ok := h.pathUUID(w, r, "priceListId", application.ErrPriceListNotFound)
	if !ok {
		return
	}
	page, err := h.svc.ListPriceItems(r.Context(), rc, listID, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(page.RowVersion))
	writeJSON(w, http.StatusOK, priceItemPageView(page))
}

// PutPriceItems implements putPriceItems; If-Match carries the ETag of the price list.
func (h *Handler) PutPriceItems(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	listID, ok := h.pathUUID(w, r, "priceListId", application.ErrPriceListNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplacePriceItemsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.PriceItemInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.PriceItemInput{
			UnitType: string(row.UnitType), PricingMethod: string(row.PricingMethod),
			ValidFrom: row.ValidFrom.Time,
		}
		in.ServiceDefinitionID = uuidText(row.ServiceDefinitionId)
		in.ServiceCategoryID = uuidText(row.ServiceCategoryId)
		in.PackageDefinitionID = uuidText(row.PackageDefinitionId)
		in.LocationID = uuidText(row.LocationId)
		// Every money field is already a string in the generated type: the contract
		// declares them as decimal strings so that no float exists in this path at all.
		in.Amount = decimal(row.Amount)
		in.Percent = percent(row.Percent)
		in.MinAmount = decimal(row.MinAmount)
		in.MaxAmount = decimal(row.MaxAmount)
		in.MemberShareAmount = decimal(row.MemberShareAmount)
		in.MemberSharePercent = percent(row.MemberSharePercent)
		if row.FormulaKey != nil {
			in.FormulaKey = *row.FormulaKey
		}
		if row.MemberShareMethod != nil {
			in.MemberShareMethod = string(*row.MemberShareMethod)
		}
		if row.Priority != nil {
			in.Priority = *row.Priority
		}
		if row.ValidTo != nil {
			to := row.ValidTo.Time
			in.ValidTo = &to
		}
		items = append(items, in)
	}

	page, err := h.svc.ReplacePriceItems(r.Context(), rc, listID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(page.RowVersion))
	writeJSON(w, http.StatusOK, priceItemPageView(page))
}

// ListPackageDefinitions implements listPackageDefinitions.
func (h *Handler) ListPackageDefinitions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	result, err := h.svc.ListPackages(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, packageListView(result.Items))
}

// PutPackageDefinitions implements putPackageDefinitions.
func (h *Handler) PutPackageDefinitions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplacePackageDefinitionsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.PackageInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.PackageInput{Code: row.Code, Name: row.Name, MinLines: row.MinLines}
		if row.InclusionRule != nil {
			in.InclusionRule = string(*row.InclusionRule)
		}
		for _, line := range row.Lines {
			in.Lines = append(in.Lines, domain.PackageLineInput{
				ServiceDefinitionID: line.ServiceDefinitionId.String(),
				IncludedQuantity:    line.IncludedQuantity,
			})
		}
		items = append(items, in)
	}

	result, err := h.svc.ReplacePackages(r.Context(), rc, versionID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, packageListView(result.Items))
}

// ListProviderQuotas implements listProviderQuotas.
func (h *Handler) ListProviderQuotas(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	result, err := h.svc.ListQuotas(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, quotaListView(result.Items))
}

// PutProviderQuotas implements putProviderQuotas.
func (h *Handler) PutProviderQuotas(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplaceProviderQuotasRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.QuotaInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.QuotaInput{
			LocationID: uuidText(row.LocationId), ServiceDefinitionID: uuidText(row.ServiceDefinitionId),
			PeriodType: string(row.PeriodType), PeriodFrom: row.PeriodFrom.Time,
			PeriodTo: row.PeriodTo.Time, Capacity: row.Capacity,
		}
		if row.AllowOverdraft != nil {
			in.AllowOverdraft = *row.AllowOverdraft
		}
		items = append(items, in)
	}

	result, err := h.svc.ReplaceQuotas(r.Context(), rc, versionID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, quotaListView(result.Items))
}

// GetPaymentTerm implements getPaymentTerm.
func (h *Handler) GetPaymentTerm(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	result, err := h.svc.GetPaymentTerm(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, paymentTermView(result.Term, result.RowVersion))
}

// PutPaymentTerm implements putPaymentTerm.
func (h *Handler) PutPaymentTerm(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutPaymentTermRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.PaymentTermInput{
		DueDays: body.DueDays, SettlementMethod: string(body.SettlementMethod),
		TaxBehaviour: string(body.TaxBehaviour),
	}
	if body.VatRate != nil {
		in.VatRate = *body.VatRate
	}
	if body.LateFeePercent != nil {
		in.LateFeePercent = *body.LateFeePercent
	}

	result, err := h.svc.PutPaymentTerm(r.Context(), rc, versionID, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, paymentTermView(result.Term, result.RowVersion))
}

func priceListView(l application.PriceListRecord) kapsorav1.PriceList {
	count := l.ItemCount
	out := kapsorav1.PriceList{
		Id: l.ID, ContractVersionId: l.ContractVersionID, Code: l.Code, Name: l.Name,
		Priority: l.Priority, WeekdayMask: l.WeekdayMask, ItemCount: &count,
		RowVersion: int(l.RowVersion),
	}
	if l.SeasonFrom != nil {
		out.SeasonFrom = &openapi_types.Date{Time: *l.SeasonFrom}
	}
	if l.SeasonTo != nil {
		out.SeasonTo = &openapi_types.Date{Time: *l.SeasonTo}
	}
	return out
}

func priceListListView(items []application.PriceListRecord) kapsorav1.PriceListList {
	out := kapsorav1.PriceListList{Items: make([]kapsorav1.PriceList, 0, len(items))}
	for _, l := range items {
		out.Items = append(out.Items, priceListView(l))
	}
	return out
}

func priceItemPageView(page application.PriceItemPage) kapsorav1.PriceItemPage {
	out := kapsorav1.PriceItemPage{Items: make([]kapsorav1.PriceItem, 0, len(page.Items))}
	for _, it := range page.Items {
		item := kapsorav1.PriceItem{
			Id: it.ID, PriceListId: it.PriceListID,
			ServiceDefinitionId: it.ServiceDefinitionID, ServiceDefinitionCode: it.ServiceDefinitionCode,
			ServiceCategoryId: it.ServiceCategoryID, ServiceCategoryCode: it.ServiceCategoryCode,
			PackageDefinitionId: it.PackageDefinitionID, PackageDefinitionCode: it.PackageDefinitionCode,
			LocationId:    it.LocationID,
			UnitType:      kapsorav1.ServiceUnitType(it.UnitType),
			PricingMethod: kapsorav1.PricingMethod(it.PricingMethod),
			Amount:        optionalString(it.Amount), Percent: optionalString(it.Percent),
			FormulaKey: it.FormulaKey,
			MinAmount:  optionalString(it.MinAmount), MaxAmount: optionalString(it.MaxAmount),
			MemberShareMethod:  kapsorav1.MemberShareMethod(it.MemberShareMethod),
			MemberShareAmount:  optionalString(it.MemberShareAmount),
			MemberSharePercent: optionalString(it.MemberSharePercent),
			ValidFrom:          openapi_types.Date{Time: it.ValidFrom},
			Priority:           it.Priority,
		}
		if it.ValidTo != nil {
			item.ValidTo = &openapi_types.Date{Time: *it.ValidTo}
		}
		out.Items = append(out.Items, item)
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	return out
}

func packageListView(items []application.PackageRecord) kapsorav1.PackageDefinitionList {
	out := kapsorav1.PackageDefinitionList{Items: make([]kapsorav1.PackageDefinition, 0, len(items))}
	for _, p := range items {
		pkg := kapsorav1.PackageDefinition{
			Id: p.ID, ContractVersionId: p.ContractVersionID, Code: p.Code, Name: p.Name,
			InclusionRule: kapsorav1.PackageInclusionRule(p.InclusionRule), MinLines: p.MinLines,
			Lines: make([]kapsorav1.PackageLine, 0, len(p.Lines)),
		}
		for _, line := range p.Lines {
			pkg.Lines = append(pkg.Lines, kapsorav1.PackageLine{
				ServiceDefinitionId: line.ServiceDefinitionID, ServiceDefinitionCode: line.ServiceDefinitionCode,
				IncludedQuantity: line.IncludedQuantity,
			})
		}
		out.Items = append(out.Items, pkg)
	}
	return out
}

func quotaListView(items []application.QuotaRecord) kapsorav1.ProviderQuotaList {
	out := kapsorav1.ProviderQuotaList{Items: make([]kapsorav1.ProviderQuota, 0, len(items))}
	for _, q := range items {
		out.Items = append(out.Items, kapsorav1.ProviderQuota{
			Id: q.ID, ContractVersionId: q.ContractVersionID,
			LocationId: q.LocationID, ServiceDefinitionId: q.ServiceDefinitionID,
			PeriodType: kapsorav1.QuotaPeriodType(q.PeriodType),
			PeriodFrom: openapi_types.Date{Time: q.PeriodFrom},
			PeriodTo:   openapi_types.Date{Time: q.PeriodTo},
			Capacity:   q.Capacity, Consumed: q.Consumed, AllowOverdraft: q.AllowOverdraft,
		})
	}
	return out
}

func paymentTermView(t application.PaymentTermRecord, rowVersion int64) kapsorav1.PaymentTerm {
	return kapsorav1.PaymentTerm{
		Id: t.ID, ContractVersionId: t.ContractVersionID, DueDays: t.DueDays,
		SettlementMethod: kapsorav1.SettlementMethod(t.SettlementMethod),
		TaxBehaviour:     kapsorav1.TaxBehaviour(t.TaxBehaviour),
		VatRate:          optionalString(t.VatRate), LateFeePercent: optionalString(t.LateFeePercent),
		RowVersion: int(rowVersion),
	}
}

// uuidText renders an optional identifier for the domain, which takes ids as strings so it
// never has to depend on a uuid implementation.
func uuidText(id *openapi_types.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// decimal and percent read the contract's decimal-string fields. They exist so that the
// "no float" rule is visible at this boundary rather than merely implied by it: the
// generated types are plain strings, and nothing here ever parses one into a number.
func decimal(v *kapsorav1.DecimalAmount) string {
	if v == nil {
		return ""
	}
	return *v
}

func percent(v *kapsorav1.DecimalPercent) string {
	if v == nil {
		return ""
	}
	return *v
}
