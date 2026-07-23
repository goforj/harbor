package projectruntime

import (
	"context"
	"net/netip"
)

// EnvironmentVariable is one explicit value a runtime provider adds to a project process.
type EnvironmentVariable struct {
	Name  string
	Value string
}

// EnvironmentFile is one provider-recognized project environment file.
type EnvironmentFile struct {
	Name     string
	Contents string
	Revision string
}

// EnvironmentInspection is the provider-owned environment surface for one checkout.
type EnvironmentInspection struct {
	OverridesAvailable bool
	OverrideError      string
	Overrides          []EnvironmentVariable
	Files              []EnvironmentFile
}

// EnvironmentInspectionRequest identifies a checkout and its retained Harbor address, when one exists.
type EnvironmentInspectionRequest struct {
	CheckoutRoot string
	Address      netip.Addr
}

// EnvironmentFileSaveRequest identifies one revision-fenced provider environment file edit.
type EnvironmentFileSaveRequest struct {
	CheckoutRoot string
	Name         string
	Contents     string
	Revision     string
}

// EnvironmentManager optionally exposes a runtime provider's environment inputs and editable files.
type EnvironmentManager interface {
	InspectEnvironment(context.Context, EnvironmentInspectionRequest) (EnvironmentInspection, error)
	SaveEnvironmentFile(context.Context, EnvironmentFileSaveRequest) (EnvironmentFile, error)
}
