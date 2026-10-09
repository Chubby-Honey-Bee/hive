package models

import "fmt"

// CostLabel renders a cost for people to read. dollars is the priced part,
// already formatted. metered and unmetered count the model calls whose cost
// is and is not known: an unmetered call's model has no price in this config,
// or its backend reports no token counts. An unmetered call adds nothing to
// the priced part because its cost is unknown, not because it was free, so the
// label is dollars alone when no call was unmetered, "unmetered" when no call
// was metered, and both when the calls were mixed.
func CostLabel(dollars string, metered, unmetered int64) string {
	switch {
	case unmetered == 0:
		return dollars
	case metered == 0:
		return "unmetered"
	default:
		return fmt.Sprintf("%s + unmetered (%d calls)", dollars, unmetered)
	}
}
