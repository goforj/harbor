package reconcile

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/goforj/harbor/internal/domain"
	"github.com/goforj/harbor/internal/goforj"
	"github.com/goforj/harbor/internal/projectdiscovery"
	"github.com/goforj/harbor/internal/projectprocess"
)

// TestDefaultRuntimeAdmitsOwnedFrameworkResources verifies optional links cannot escape the observed App and service topology.
func TestDefaultRuntimeAdmitsOwnedFrameworkResources(t *testing.T) {
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", netip.MustParseAddr("127.77.4.8"), 3000)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	services := []domain.ServiceSnapshot{
		{ID: "grafana", Name: "Grafana", Kind: "compose", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected},
		{ID: "mailpit", Name: "Mailpit", Kind: "compose", State: domain.EntityReady, Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected},
	}
	observation := projectprocess.FrameworkResourceObservation{
		Supported: true,
		Resources: []projectprocess.FrameworkResource{
			{ID: "app", Name: "App", Kind: "app", URL: target.ResourceURL + "/", App: "app"},
			{ID: "api-reference", Name: "API Reference", Kind: "api-reference", URL: target.ResourceURL + "/swagger", App: "app"},
			{ID: "lighthouse", Name: "Lighthouse", Kind: "development", URL: target.ResourceURL + "/lighthouse", App: "app"},
			{ID: "mailpit", Name: "Mailpit", Kind: "mail", URL: "http://127.77.4.8:8025", Service: "mailpit"},
			{ID: "grafana", Name: "Grafana", Kind: "metrics", URL: "http://127.77.4.8:3001", Service: "grafana"},
			{ID: "unknown-service", Name: "Unknown", Kind: "service", URL: "http://127.77.4.8:9000", Service: "unknown"},
			{ID: "unknown-app", Name: "Unknown App", Kind: "app", URL: "http://127.77.4.8:9001", App: "worker"},
			{ID: "app-http", Name: "Conflicting App", Kind: "app", URL: "http://127.77.4.8:9002", App: "app"},
		},
	}

	runtime := defaultRuntime(target, services, projectprocess.ProjectDescriptorObservation{}, observation)
	if err := runtime.Validate(); err != nil {
		t.Fatalf("DefaultProjectRuntime.Validate() error = %v", err)
	}
	identities := make([]domain.ResourceID, 0, len(runtime.Resources))
	for _, resource := range runtime.Resources {
		identities = append(identities, resource.ID)
	}
	want := []domain.ResourceID{"api-reference", "app-http", "grafana", "lighthouse", "mailpit"}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("resource identities = %#v, want %#v", identities, want)
	}
	if runtime.Resources[1].URL != target.ResourceURL || runtime.Resources[1].Owner.AppID != target.AppID {
		t.Fatalf("proven App resource = %#v", runtime.Resources[1])
	}
}

// TestDefaultRuntimeAdmitsOnlyDescriptorMatchedResources keeps live links inside the static owner, runtime, and path contract.
func TestDefaultRuntimeAdmitsOnlyDescriptorMatchedResources(t *testing.T) {
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", netip.MustParseAddr("127.77.4.8"), 3000)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	services := []domain.ServiceSnapshot{{
		ID: "mailpit", Name: "Mailpit", Kind: "compose", State: domain.EntityReady,
		Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
	}}
	descriptor := projectprocess.ProjectDescriptorObservation{
		ResourcesSupported: true,
		Resources: []goforj.Resource{
			{ID: "api-reference", Name: "API Reference", Category: "docs", Protocol: goforj.ResourceProtocolHTTP, Owner: goforj.ResourceOwnerApp, App: "app", Runtime: "http", Path: "/swagger", Enabled: true},
			{ID: "mailpit", Name: "Mailpit", Category: "mail", Protocol: goforj.ResourceProtocolHTTP, Owner: goforj.ResourceOwnerService, Service: "mailpit", Runtime: "http", Path: "/", Enabled: true},
		},
	}
	observation := projectprocess.FrameworkResourceObservation{
		Supported: true,
		Resources: []projectprocess.FrameworkResource{
			{ID: "api-reference", Name: "old name", Kind: "old-kind", URL: target.ResourceURL + "/swagger", App: "app", Runtime: "http"},
			{ID: "mailpit", Name: "Mailpit", Kind: "mail", URL: "http://127.77.4.8:8025/", Service: "mailpit", Runtime: "http"},
			{ID: "wrong-runtime", Name: "Wrong runtime", Kind: "docs", URL: target.ResourceURL + "/swagger", App: "app", Runtime: "worker"},
			{ID: "wrong-path", Name: "Wrong path", Kind: "docs", URL: target.ResourceURL + "/other", App: "app", Runtime: "http"},
		},
	}

	runtime := defaultRuntime(target, services, descriptor, observation)
	if err := runtime.Validate(); err != nil {
		t.Fatalf("DefaultProjectRuntime.Validate() error = %v", err)
	}
	if len(runtime.Resources) != 3 || runtime.Resources[0].ID != "api-reference" || runtime.Resources[1].ID != "app-http" || runtime.Resources[2].ID != "mailpit" {
		t.Fatalf("descriptor resources = %#v, want api-reference, app-http, and mailpit", runtime.Resources)
	}
	if got := runtime.Resources[0]; got.Name != "API Reference" || got.Kind != "docs" || got.Owner.AppID != "app" {
		t.Fatalf("descriptor projection = %#v", got)
	}
	if got := runtime.Resources[2]; got.URL != "http://127.77.4.8:8025" || got.Owner.ServiceID != "mailpit" {
		t.Fatalf("canonical service resource = %#v", got)
	}
}

// TestFrameworkResourceMatchesDescriptorCoversOwnerRuntimeAndPathJoins prevents a live report from crossing static intent.
func TestFrameworkResourceMatchesDescriptorCoversOwnerRuntimeAndPathJoins(t *testing.T) {
	appIntent := goforj.Resource{Protocol: goforj.ResourceProtocolHTTP, Owner: goforj.ResourceOwnerApp, App: "app", Runtime: "http", Path: "/swagger"}
	serviceIntent := goforj.Resource{Protocol: goforj.ResourceProtocolHTTP, Owner: goforj.ResourceOwnerService, Service: "mailpit", Runtime: "http", Path: "/"}
	tests := []struct {
		name     string
		reported projectprocess.FrameworkResource
		intent   goforj.Resource
		want     bool
	}{
		{name: "App owner", reported: projectprocess.FrameworkResource{App: "app", Runtime: "http", URL: "http://127.77.4.8:3000/swagger"}, intent: appIntent, want: true},
		{name: "service owner", reported: projectprocess.FrameworkResource{Service: "mailpit", Runtime: "http", URL: "http://127.77.4.8:8025/"}, intent: serviceIntent, want: true},
		{name: "wrong owner", reported: projectprocess.FrameworkResource{Service: "other", Runtime: "http", URL: "http://127.77.4.8:8025/"}, intent: serviceIntent},
		{name: "cross owner field", reported: projectprocess.FrameworkResource{App: "app", Service: "mailpit", Runtime: "http", URL: "http://127.77.4.8:8025/"}, intent: serviceIntent},
		{name: "wrong runtime", reported: projectprocess.FrameworkResource{App: "app", Runtime: "worker", URL: "http://127.77.4.8:3000/swagger"}, intent: appIntent},
		{name: "wrong path", reported: projectprocess.FrameworkResource{App: "app", Runtime: "http", URL: "http://127.77.4.8:3000/other"}, intent: appIntent},
		{name: "query decoration", reported: projectprocess.FrameworkResource{App: "app", Runtime: "http", URL: "http://127.77.4.8:3000/swagger?raw=1"}, intent: appIntent},
		{name: "HTTPS URL", reported: projectprocess.FrameworkResource{App: "app", Runtime: "http", URL: "https://127.77.4.8:3000/swagger"}, intent: appIntent},
		{name: "unsupported protocol", reported: projectprocess.FrameworkResource{App: "app", Runtime: "http", URL: "http://127.77.4.8:3000/swagger"}, intent: goforj.Resource{Protocol: "tcp", Owner: goforj.ResourceOwnerApp, App: "app", Runtime: "http", Path: "/swagger"}},
		{name: "unsupported owner", reported: projectprocess.FrameworkResource{App: "app", Runtime: "http", URL: "http://127.77.4.8:3000/swagger"}, intent: goforj.Resource{Protocol: goforj.ResourceProtocolHTTP, Owner: "other", App: "app", Runtime: "http", Path: "/swagger"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := frameworkResourceMatchesDescriptor(test.reported, test.intent); got != test.want {
				t.Fatalf("frameworkResourceMatchesDescriptor() = %t, want %t", got, test.want)
			}
		})
	}
}

// TestDefaultRuntimeKeepsProvenAppResourceWithoutFrameworkSupport verifies older GoForj releases remain startable.
func TestDefaultRuntimeKeepsProvenAppResourceWithoutFrameworkSupport(t *testing.T) {
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", netip.MustParseAddr("127.77.4.16"), 3000)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	runtime := defaultRuntime(
		target,
		[]domain.ServiceSnapshot{},
		projectprocess.ProjectDescriptorObservation{},
		projectprocess.FrameworkResourceObservation{Supported: false, Resources: []projectprocess.FrameworkResource{}},
	)
	if len(runtime.Resources) != 1 || runtime.Resources[0].ID != "app-http" {
		t.Fatalf("defaultRuntime() resources = %#v", runtime.Resources)
	}
	if runtime.Resources == nil {
		t.Fatal("defaultRuntime() resources = nil")
	}
	if err := runtime.Validate(); err != nil {
		t.Fatalf("DefaultProjectRuntime.Validate() error = %v", err)
	}
}

// TestDefaultRuntimeRejectsFrameworkResourcesOutsideAssignedAddress prevents configured public URLs from corrupting pre-full runtime state.
func TestDefaultRuntimeRejectsFrameworkResourcesOutsideAssignedAddress(t *testing.T) {
	target, err := projectdiscovery.NewRuntimeTarget("app", "App", netip.MustParseAddr("127.77.4.24"), 3000)
	if err != nil {
		t.Fatalf("NewRuntimeTarget() error = %v", err)
	}
	runtime := defaultRuntime(
		target,
		[]domain.ServiceSnapshot{{
			ID: "mailpit", Name: "Mailpit", Kind: "compose", State: domain.EntityReady,
			Owner: domain.ServiceOwnerCompose, Selection: domain.ServiceSelected,
		}},
		projectprocess.ProjectDescriptorObservation{},
		projectprocess.FrameworkResourceObservation{
			Supported: true,
			Resources: []projectprocess.FrameworkResource{
				{ID: "api", Name: "API", Kind: "api", URL: "https://dev.diclan.app", App: "app"},
				{ID: "swagger", Name: "Swagger", Kind: "docs", URL: "https://dev.diclan.app/swagger", App: "app"},
				{ID: "localhost-tool", Name: "Localhost Tool", Kind: "tool", URL: "http://localhost:9000", App: "app"},
				{ID: "other-loopback", Name: "Other Loopback", Kind: "tool", URL: "http://127.77.4.25:9000", Service: "mailpit"},
				{ID: "lighthouse", Name: "Lighthouse", Kind: "operator", URL: target.ResourceURL + "/lighthouse", App: "app"},
			},
		},
	)

	identities := make([]domain.ResourceID, 0, len(runtime.Resources))
	for _, resource := range runtime.Resources {
		identities = append(identities, resource.ID)
	}
	want := []domain.ResourceID{"app-http", "lighthouse"}
	if !reflect.DeepEqual(identities, want) {
		t.Fatalf("resource identities = %#v, want %#v", identities, want)
	}
	if err := runtime.Validate(); err != nil {
		t.Fatalf("DefaultProjectRuntime.Validate() error = %v", err)
	}
}

// TestEquivalentHTTPResourceURLDistinguishesLaunchTargets protects non-primary paths, queries, and hosts from deduplication.
func TestEquivalentHTTPResourceURLDistinguishesLaunchTargets(t *testing.T) {
	if !equivalentHTTPResourceURL("http://127.77.4.8:3000/", "http://127.77.4.8:3000") {
		t.Fatal("equivalentHTTPResourceURL() rejected optional trailing slash")
	}
	for _, candidate := range []string{
		"http://127.77.4.8:3000/swagger",
		"http://127.77.4.8:3000?view=full",
		"http://127.77.4.9:3000",
	} {
		if equivalentHTTPResourceURL(candidate, "http://127.77.4.8:3000") {
			t.Fatalf("equivalentHTTPResourceURL(%q) = true", candidate)
		}
	}
}
