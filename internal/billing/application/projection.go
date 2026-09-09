package application

// The projection here is WP-I5-01's, applied to this package's records. The *decision* about
// which projection a caller has earned is not made here — it is `healthapp.DecideProjection`,
// asked in exactly one place — and what follows is only the clearing.
//
// Clearing rather than "not rendering" is deliberate and is the rule WP-I5-01 wrote down: a
// record that no longer carries the answer cannot leak it, however it is later serialised. A
// mapper that remembered to omit a field is a mapper somebody will later change.

// projectAllocation returns one claim link as the caller may see it.
//
// One field goes, and it is the one v1.2 §2.11 names: `ClaimDescription`, the provider's own
// words on the claim's lines. "Sol diz artroskopi sonrası kontrol" is a diagnosis in a
// sentence, and the provider typing a line description is not thinking about who will read it
// months later on an invoice.
//
// Everything else survives both projections, and has to. The claim reference, the version, the
// allocated amount, the approved total, the currency and the claim's status are what an
// invoice *is*: a sponsor's HR user checking that the bill for their members' claims is the
// bill the payer approved needs every one of them, and could do nothing with a record that had
// the money taken out of it.
func projectAllocation(rec AllocationRecord, p Projection) AllocationRecord {
	if p == ProjectionClinical {
		return rec
	}
	rec.ClaimDescription = nil
	return rec
}

// projectAllocations applies projectAllocation over a slice, returning a new one so the
// caller's rows are never mutated in place.
func projectAllocations(rows []AllocationRecord, p Projection) []AllocationRecord {
	out := make([]AllocationRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectAllocation(row, p))
	}
	return out
}
