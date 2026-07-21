package managedsession

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/goforj/harbor/internal/harbordruntime"
)

const (
	maximumRuntimePlanRoutes    = 32
	maximumRuntimePlanEndpoints = 256
	minimumRuntimePlanPort      = 1024
)

// RuntimePlanRequest asks Harbor for assignments for one exact attached session.
type RuntimePlanRequest struct {
	SchemaVersion uint16                                 `json:"schema_version"`
	Fence         harbordruntime.ManagedPublicationFence `json:"fence"`
	ActiveApps    []ActiveApp                            `json:"active_apps"`
}

// Validate reports whether a runtime-plan request is bounded and tied to one session fence.
func (request RuntimePlanRequest) Validate() error {
	if request.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed runtime plan request schema version %d is unsupported", request.SchemaVersion)
	}
	if err := request.Fence.Validate(); err != nil {
		return err
	}
	if request.ActiveApps == nil {
		return errors.New("managed runtime plan active Apps must be initialized")
	}
	if len(request.ActiveApps) > maximumManagedSessionApps {
		return fmt.Errorf("managed runtime plan contains more than %d Apps", maximumManagedSessionApps)
	}
	for index, app := range request.ActiveApps {
		if err := app.Validate(); err != nil {
			return fmt.Errorf("managed runtime plan App %d: %w", index+1, err)
		}
		if index > 0 && request.ActiveApps[index-1].ID >= app.ID {
			return errors.New("managed runtime plan active Apps must be sorted and unique")
		}
	}
	return nil
}

// RuntimePlanApp assigns private listeners and public resource intent to one App.
type RuntimePlanApp struct {
	ID       string               `json:"id"`
	Active   bool                 `json:"active"`
	Runtimes []RuntimePlanRuntime `json:"runtimes"`
}

// Validate reports whether one App assignment has deterministic runtime ordering and bounded listeners.
func (app RuntimePlanApp) Validate() error {
	if err := validateManagedSessionToken("managed runtime plan App ID", app.ID, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if app.Runtimes == nil {
		return fmt.Errorf("managed runtime plan App %q runtimes must be initialized", app.ID)
	}
	if len(app.Runtimes) > maximumManagedSessionRuntimes {
		return fmt.Errorf("managed runtime plan App %q contains more than %d runtimes", app.ID, maximumManagedSessionRuntimes)
	}
	for index, runtime := range app.Runtimes {
		if err := runtime.Validate(); err != nil {
			return fmt.Errorf("managed runtime plan App %q runtime %d: %w", app.ID, index+1, err)
		}
		if index > 0 && app.Runtimes[index-1].ID >= runtime.ID {
			return fmt.Errorf("managed runtime plan App %q runtimes must be sorted and unique", app.ID)
		}
	}
	return nil
}

// RuntimePlanRuntime assigns one private App listener and its user-facing URL/routes.
type RuntimePlanRuntime struct {
	ID        string             `json:"id"`
	BindHost  string             `json:"bind_host"`
	BindPort  uint16             `json:"bind_port"`
	PublicURL string             `json:"public_url,omitempty"`
	Routes    []RuntimePlanRoute `json:"routes"`
}

// Validate reports whether one runtime assignment cannot escape loopback or contain an unsafe URL.
func (runtime RuntimePlanRuntime) Validate() error {
	if err := validateManagedSessionToken("managed runtime ID", runtime.ID, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if err := validateRuntimePlanLoopbackHost("managed runtime bind host", runtime.BindHost); err != nil {
		return err
	}
	if runtime.BindPort < minimumRuntimePlanPort {
		return fmt.Errorf("managed runtime %q bind port must be at least %d", runtime.ID, minimumRuntimePlanPort)
	}
	if runtime.PublicURL != "" {
		if err := validateRuntimePlanPublicURL(runtime.PublicURL); err != nil {
			return fmt.Errorf("managed runtime %q public URL: %w", runtime.ID, err)
		}
	}
	if runtime.Routes == nil {
		return fmt.Errorf("managed runtime %q routes must be initialized", runtime.ID)
	}
	if len(runtime.Routes) > maximumRuntimePlanRoutes {
		return fmt.Errorf("managed runtime %q contains more than %d routes", runtime.ID, maximumRuntimePlanRoutes)
	}
	for index, route := range runtime.Routes {
		if err := route.Validate(); err != nil {
			return fmt.Errorf("managed runtime %q route %d: %w", runtime.ID, index+1, err)
		}
		if index > 0 && runtime.Routes[index-1].Name >= route.Name {
			return fmt.Errorf("managed runtime %q routes must be sorted and unique", runtime.ID)
		}
	}
	return nil
}

// RuntimePlanRoute assigns one stable semantic route name to an HTTP path.
type RuntimePlanRoute struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Validate reports whether one route has a stable name and absolute local path.
func (route RuntimePlanRoute) Validate() error {
	if err := validateManagedSessionToken("managed runtime route name", route.Name, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if route.Path == "" || !strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "\r\n") {
		return fmt.Errorf("managed runtime route %q path must be an absolute path", route.Name)
	}
	if len(route.Path) > maximumManagedSessionTokenBytes || !utf8.ValidString(route.Path) {
		return fmt.Errorf("managed runtime route %q path is not bounded UTF-8", route.Name)
	}
	return nil
}

// RuntimePlanServiceEndpoint assigns one private publication and one native service endpoint.
type RuntimePlanServiceEndpoint struct {
	ID            string   `json:"id"`
	RequirementID string   `json:"requirement_id"`
	Consumers     []string `json:"consumers"`
	PublishHost   string   `json:"publish_host"`
	PublishPort   uint16   `json:"publish_port"`
	PublicHost    string   `json:"public_host"`
	PublicPort    uint16   `json:"public_port"`
}

// Validate reports whether one service assignment is deterministic and keeps private addressing loopback-only.
func (endpoint RuntimePlanServiceEndpoint) Validate() error {
	if err := validateManagedSessionToken("managed runtime endpoint ID", endpoint.ID, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if err := validateManagedSessionToken("managed runtime requirement ID", endpoint.RequirementID, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if endpoint.Consumers == nil {
		return fmt.Errorf("managed runtime endpoint %q consumers must be initialized", endpoint.ID)
	}
	for index, consumer := range endpoint.Consumers {
		if err := validateManagedSessionToken("managed runtime endpoint consumer", consumer, maximumManagedSessionTokenBytes); err != nil {
			return err
		}
		if index > 0 && endpoint.Consumers[index-1] >= consumer {
			return fmt.Errorf("managed runtime endpoint %q consumers must be sorted and unique", endpoint.ID)
		}
	}
	if err := validateRuntimePlanLoopbackHost("managed runtime endpoint publish host", endpoint.PublishHost); err != nil {
		return err
	}
	if endpoint.PublishPort < minimumRuntimePlanPort {
		return fmt.Errorf("managed runtime endpoint %q publish port must be at least %d", endpoint.ID, minimumRuntimePlanPort)
	}
	if err := validateManagedSessionToken("managed runtime endpoint public host", endpoint.PublicHost, maximumManagedSessionTokenBytes); err != nil {
		return err
	}
	if endpoint.PublicPort == 0 {
		return fmt.Errorf("managed runtime endpoint %q public port must be positive", endpoint.ID)
	}
	return nil
}

// RuntimePlan is the complete replacement assignment set for one managed session.
type RuntimePlan struct {
	Apps             []RuntimePlanApp             `json:"apps"`
	ServiceEndpoints []RuntimePlanServiceEndpoint `json:"service_endpoints"`
}

// Validate reports whether plan collections are initialized, deterministic, and duplicate-free.
func (plan RuntimePlan) Validate() error {
	if plan.Apps == nil || plan.ServiceEndpoints == nil {
		return errors.New("managed runtime plan collections must be initialized")
	}
	if len(plan.Apps) > maximumManagedSessionApps {
		return fmt.Errorf("managed runtime plan contains more than %d Apps", maximumManagedSessionApps)
	}
	if len(plan.ServiceEndpoints) > maximumRuntimePlanEndpoints {
		return fmt.Errorf("managed runtime plan contains more than %d service endpoints", maximumRuntimePlanEndpoints)
	}
	for index, app := range plan.Apps {
		if err := app.Validate(); err != nil {
			return fmt.Errorf("managed runtime plan App %d: %w", index+1, err)
		}
		if index > 0 && plan.Apps[index-1].ID >= app.ID {
			return errors.New("managed runtime plan Apps must be sorted and unique")
		}
	}
	for index, endpoint := range plan.ServiceEndpoints {
		if err := endpoint.Validate(); err != nil {
			return fmt.Errorf("managed runtime plan endpoint %d: %w", index+1, err)
		}
		if index > 0 && plan.ServiceEndpoints[index-1].ID >= endpoint.ID {
			return errors.New("managed runtime plan service endpoints must be sorted and unique")
		}
	}
	return nil
}

// RuntimePlanResponse returns one complete assignment plan correlated to an attached session.
type RuntimePlanResponse struct {
	SchemaVersion uint16                                 `json:"schema_version"`
	Fence         harbordruntime.ManagedPublicationFence `json:"fence"`
	Plan          RuntimePlan                            `json:"plan"`
}

// Validate reports whether a runtime-plan response contains bounded assignments and a complete fence.
func (response RuntimePlanResponse) Validate() error {
	if response.SchemaVersion != SchemaVersion {
		return fmt.Errorf("managed runtime plan response schema version %d is unsupported", response.SchemaVersion)
	}
	if err := response.Fence.Validate(); err != nil {
		return err
	}
	return response.Plan.Validate()
}

// ValidateRuntimePlanCorrelation binds an assignment plan to one exact request fence and App set.
func ValidateRuntimePlanCorrelation(request RuntimePlanRequest, response RuntimePlanResponse) error {
	if err := request.Validate(); err != nil {
		return fmt.Errorf("validate managed runtime plan request: %w", err)
	}
	if err := response.Validate(); err != nil {
		return fmt.Errorf("validate managed runtime plan response: %w", err)
	}
	if response.Fence != request.Fence {
		return errors.New("managed runtime plan response does not match the request fence")
	}
	requested := make(map[string]ActiveApp, len(request.ActiveApps))
	for _, app := range request.ActiveApps {
		requested[string(app.ID)] = app
	}
	for _, app := range response.Plan.Apps {
		requestedApp, found := requested[app.ID]
		if !found {
			return fmt.Errorf("managed runtime plan contains unrequested App %q", app.ID)
		}
		if !app.Active {
			return fmt.Errorf("managed runtime plan App %q is not active", app.ID)
		}
		runtimeIDs := make([]string, 0, len(app.Runtimes))
		for _, runtime := range app.Runtimes {
			runtimeIDs = append(runtimeIDs, runtime.ID)
		}
		if !slices.Equal(runtimeIDs, requestedApp.RuntimeIDs) {
			return fmt.Errorf("managed runtime plan App %q runtime set does not match the request", app.ID)
		}
		delete(requested, app.ID)
	}
	if len(requested) != 0 {
		return errors.New("managed runtime plan is missing one or more requested Apps")
	}
	return nil
}

// MarshalRuntimePlanRequest validates and encodes one runtime-plan request.
func MarshalRuntimePlanRequest(request RuntimePlanRequest) ([]byte, error) {
	return marshalManagedSessionObject("managed runtime plan request", request, request.Validate)
}

// DecodeRuntimePlanRequest strictly decodes and validates one runtime-plan request.
func DecodeRuntimePlanRequest(payload []byte) (RuntimePlanRequest, error) {
	var request RuntimePlanRequest
	if err := decodeManagedSessionObject(payload, "managed runtime plan request", &request); err != nil {
		return RuntimePlanRequest{}, err
	}
	if err := request.Validate(); err != nil {
		return RuntimePlanRequest{}, err
	}
	return request, nil
}

// MarshalRuntimePlanResponse validates and encodes one runtime-plan response.
func MarshalRuntimePlanResponse(response RuntimePlanResponse) ([]byte, error) {
	return marshalManagedSessionObject("managed runtime plan response", response, response.Validate)
}

// DecodeRuntimePlanResponse strictly decodes and validates one runtime-plan response.
func DecodeRuntimePlanResponse(payload []byte) (RuntimePlanResponse, error) {
	var response RuntimePlanResponse
	if err := decodeManagedSessionObject(payload, "managed runtime plan response", &response); err != nil {
		return RuntimePlanResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return RuntimePlanResponse{}, err
	}
	return response, nil
}

// validateRuntimePlanLoopbackHost restricts private assignments to canonical IPv4 loopback addresses.
func validateRuntimePlanLoopbackHost(name, host string) error {
	address := net.ParseIP(host)
	if address == nil || address.To4() == nil || !address.IsLoopback() || address.To4().String() != host {
		return fmt.Errorf("%s must be canonical IPv4 loopback", name)
	}
	return nil
}

// validateRuntimePlanPublicURL accepts only credential-free HTTP(S) browser origins and paths.
func validateRuntimePlanPublicURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be an absolute credential-free HTTP(S) URL")
	}
	if parsed.Port() != "" {
		for _, character := range parsed.Port() {
			if character < '0' || character > '9' {
				return errors.New("must use a numeric port")
			}
		}
	}
	return nil
}
