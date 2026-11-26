package registry

// ProvimuxPublishedListsInForce reports models.published-lists of the config in force, off in Home mode: the
// process's catalog policy as the service last set it (StartModelCatalogUpdaters at start,
// UpdateModelCatalogSources at each committed config). Provimux: the client list's `figures`
// (prices-and-cache.md 5 and 6.2), whose handler has no config of its own. Before the service sets a policy it is
// the unset value: on, except in a test binary.
func ProvimuxPublishedListsInForce() bool {
	catalogRuntime.Lock()
	sources, home := catalogRuntime.sources, catalogRuntime.home
	catalogRuntime.Unlock()
	return sources.PublishedListsOn() && !home
}
