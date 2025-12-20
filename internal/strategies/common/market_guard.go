package common

// MarketSlugGuard tracks current market slug and detects changes.
//
// It is intentionally tiny; strategies can decide what "reset" means on change.
type MarketSlugGuard struct {
	slug string
}

// Update sets the current slug and returns true if it changed (including first set).
func (g *MarketSlugGuard) Update(slug string) (changed bool) {
	changed = slug != g.slug
	g.slug = slug
	return changed
}

func (g *MarketSlugGuard) Current() string { return g.slug }
