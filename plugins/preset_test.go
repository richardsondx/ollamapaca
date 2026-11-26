package plugins

import "testing"

// TestBundledPresets loads the plugin folders shipped in this repo:
// every one must load, and none is enabled until the user enables it.
func TestBundledPresets(t *testing.T) {
	reg := NewRegistry(".")
	if errs := reg.Errors(); len(errs) != 0 {
		t.Fatalf("bundled plugins failed to load: %v", errs)
	}
	want := []string{"browser", "computer", "cred", "dispatch", "evidence", "exa", "expedia", "firecrawl", "github", "gmail", "google-calendar", "google-drive", "vercel"}
	got := map[string]Plugin{}
	for _, p := range reg.List() {
		got[p.Name] = p
		if p.Enabled {
			t.Fatalf("%s must be disabled by default", p.Name)
		}
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("missing bundled plugin %s", name)
		}
	}
	if len(reg.EnabledTools()) != 0 {
		t.Fatalf("no tools before anything is enabled")
	}
	if got["gmail"].MCPServers["gmail"].OAuth == nil || got["vercel"].MCPServers["vercel"].OAuth == nil {
		t.Fatalf("OAuth presets lost their oauth block")
	}
}
