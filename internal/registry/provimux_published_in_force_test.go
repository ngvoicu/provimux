package registry

import "testing"

// Step F3: the client list's switch is the policy the service last put in force, off in Home mode.
func TestProvimuxPublishedListsInForce(t *testing.T) {
	catalogRuntime.Lock()
	previous, previousHome := catalogRuntime.sources, catalogRuntime.home
	catalogRuntime.Unlock()
	t.Cleanup(func() {
		catalogRuntime.Lock()
		catalogRuntime.sources, catalogRuntime.home = previous, previousHome
		catalogRuntime.Unlock()
	})
	on, off := true, false
	for _, tc := range []struct {
		name    string
		sources CatalogSources
		home    bool
		want    bool
	}{
		{"unset in a test binary", CatalogSources{}, false, false},
		{"on", CatalogSources{PublishedLists: &on}, false, true},
		{"off", CatalogSources{PublishedLists: &off}, false, false},
		{"on in Home mode", CatalogSources{PublishedLists: &on}, true, false},
	} {
		UpdateModelCatalogSources(tc.sources, tc.home)
		if got := ProvimuxPublishedListsInForce(); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
