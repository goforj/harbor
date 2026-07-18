package main

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/goforj/harbor/desktop/internal/desktopwire"
	"github.com/goforj/harbor/desktop/internal/wirefixture"
)

// TestFrontendWireFixtureMatchesGoGenerator prevents checked-in TypeScript from drifting from production Go wire types.
func TestFrontendWireFixtureMatchesGoGenerator(t *testing.T) {
	t.Parallel()

	want, err := wirefixture.TypeScript()
	if err != nil {
		t.Fatalf("wirefixture.TypeScript() error = %v", err)
	}
	got, err := os.ReadFile("frontend/src/bridge/harbor.fixture.ts")
	if err != nil {
		t.Fatalf("read generated fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("frontend fixture differs from the authoritative Go generator; run go generate ./...")
	}
}

// TestAppMethodsMatchFrontendContract proves the full exported Wails surface keeps its exact Go-owned arity and types.
func TestAppMethodsMatchFrontendContract(t *testing.T) {
	t.Parallel()

	document := wirefixture.Fixture()
	if err := document.Validate(); err != nil {
		t.Fatalf("wire fixture validation error = %v", err)
	}
	appType := reflect.TypeOf(&App{})
	contracts := desktopwire.MethodContracts()
	if appType.NumMethod() != len(contracts) {
		t.Fatalf("App exported method count = %d, want %d", appType.NumMethod(), len(contracts))
	}
	for _, contract := range contracts {
		method, exists := appType.MethodByName(contract.Name)
		if !exists {
			t.Errorf("App method %q does not exist", contract.Name)
			continue
		}
		if method.Type.NumIn() != contract.Signature.NumIn()+1 {
			t.Errorf("App.%s input count = %d, want receiver plus %d parameters", contract.Name, method.Type.NumIn(), contract.Signature.NumIn())
			continue
		}
		for index := range contract.Signature.NumIn() {
			if method.Type.In(index+1) != contract.Signature.In(index) {
				t.Errorf("App.%s parameter %d type = %s, want %s", contract.Name, index, method.Type.In(index+1), contract.Signature.In(index))
			}
		}
		if method.Type.NumOut() != contract.Signature.NumOut() {
			t.Errorf("App.%s output count = %d, want %d", contract.Name, method.Type.NumOut(), contract.Signature.NumOut())
			continue
		}
		for index := range contract.Signature.NumOut() {
			if method.Type.Out(index) != contract.Signature.Out(index) {
				t.Errorf("App.%s result %d type = %s, want %s", contract.Name, index, method.Type.Out(index), contract.Signature.Out(index))
			}
		}
	}
}
