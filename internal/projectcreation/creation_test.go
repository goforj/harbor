package projectcreation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testSchema provides a small producer contract for input and transport failure tests.
func testSchema() Schema {
	return Schema{SchemaVersion: 1, Fields: []Field{
		{Key: "name", Label: "Name", Step: "Project", Kind: "text", Required: true},
		{Key: "module", Label: "Module", Step: "Project", Kind: "text", Required: true},
		{Key: "path", Label: "Path", Step: "Project", Kind: "path", Required: true},
		{Key: "components", Label: "Components", Step: "Stack", Kind: "multi", Options: []Option{{Value: "cli"}}},
		{Key: "starter-kit", Label: "Starter", Step: "Stack", Kind: "select", Options: []Option{{Value: "none"}}},
		{Key: "demo", Label: "Demo", Step: "Options", Kind: "boolean"},
	}}
}

// testRequest creates a complete graphical request, including values beginning with shell metacharacters.
func testRequest(t *testing.T) Request {
	t.Helper()
	return Request{SchemaVersion: 1, Options: map[string]string{"name": "A $(not-a-command)", "module": "example.com/app", "path": filepath.Join(t.TempDir(), "new project"), "components": "cli", "starter-kit": "none", "demo": "false"}}
}

// TestCreationArgumentsRemainLiteral checks that user strings remain one argv entry each.
func TestCreationArgumentsRemainLiteral(t *testing.T) {
	request := testRequest(t)
	args, err := goForjArguments(testSchema(), request)
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "new" || args[1] != "--no-interaction" || args[2] != "--name=A $(not-a-command)" {
		t.Fatalf("unsafe argv: %q", args)
	}
	if len(args) != 8 {
		t.Fatal(args)
	}
}

// TestCreationRejectsInvalidRequests covers every user-value admission branch before process creation.
func TestCreationRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Request)
	}{
		{"version", func(r *Request) { r.SchemaVersion = 2 }},
		{"missing", func(r *Request) { delete(r.Options, "name") }},
		{"empty required", func(r *Request) { r.Options["name"] = " " }},
		{"long", func(r *Request) { r.Options["name"] = strings.Repeat("a", 4097) }},
		{"NUL", func(r *Request) { r.Options["name"] = "a\x00b" }},
		{"select", func(r *Request) { r.Options["starter-kit"] = "unknown" }},
		{"multi", func(r *Request) { r.Options["components"] = "cli,unknown" }},
		{"boolean", func(r *Request) { r.Options["demo"] = "yes" }},
		{"extra flag", func(r *Request) { r.Options["allow-non-empty"] = "true" }},
		{"relative path", func(r *Request) { r.Options["path"] = "relative" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			r := testRequest(t)
			test.change(&r)
			if _, err := goForjArguments(testSchema(), r); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

// TestCreationRejectsUnsupportedSchemas keeps unknown or dangerous producer shapes out of argv construction.
func TestCreationRejectsUnsupportedSchemas(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Schema)
	}{
		{"version", func(s *Schema) { s.SchemaVersion = 2 }},
		{"count", func(s *Schema) { s.Fields = nil }},
		{"duplicate", func(s *Schema) { s.Fields[1].Key = "name" }},
		{"bad key", func(s *Schema) { s.Fields[0].Key = "--help" }},
		{"unsafe flag", func(s *Schema) { s.Fields[0].Key = "allow-non-empty" }},
		{"kind", func(s *Schema) { s.Fields[0].Kind = "script" }},
		{"label", func(s *Schema) { s.Fields[0].Label = "" }},
		{"identity", func(s *Schema) { s.Fields[0].Key = "other" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := testSchema()
			test.change(&s)
			if err := s.Validate(); err == nil && validateGoForjSchema(s) == nil {
				t.Fatal("invalid schema accepted")
			}
		})
	}
}

// TestCreatorRechecksSchemaAndPreservesOutput verifies authoritative creation ordering and useful failure results.
func TestCreatorRechecksSchemaAndPreservesOutput(t *testing.T) {
	encoded, err := json.Marshal(struct {
		SchemaVersion int     `json:"schema_version"`
		GoForjVersion string  `json:"goforj_version"`
		Fields        []Field `json:"fields"`
	}{1, "test", testSchema().Fields})
	if err != nil {
		t.Fatal(err)
	}
	calls := [][]string{}
	creator := &Creator{executable: "/canonical/forj", run: func(_ context.Context, path string, args []string) ([]byte, error) {
		if path != "/canonical/forj" {
			t.Fatal(path)
		}
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"new", "--schema"}) {
			return encoded, nil
		}
		return []byte("renderer failed after creating files"), errors.New("exit 1")
	}}
	creator.stream = func(ctx context.Context, path string, args []string, writer io.Writer) error {
		data, err := creator.run(ctx, path, args)
		_, _ = writer.Write(data)
		return err
	}
	output, err := creator.Create(context.Background(), testRequest(t))
	if err == nil || !strings.Contains(output, "renderer failed") || output == "" {
		t.Fatalf("output=%s error=%v", output, err)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], []string{"new", "--schema"}) {
		t.Fatal(calls)
	}
	creator.run = func(_ context.Context, _ string, args []string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"new", "--schema"}) {
			return encoded, nil
		}
		return []byte("created successfully"), nil
	}
	if output, err = creator.Create(context.Background(), testRequest(t)); err != nil || output != "created successfully" {
		t.Fatalf("output=%q error=%v", output, err)
	}
	creator.run = func(context.Context, string, []string) ([]byte, error) {
		return append(append([]byte(nil), encoded...), []byte(` {"extra":true}`)...), nil
	}
	if _, err = creator.Schema(context.Background()); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatal(err)
	}
	creator.run = func(context.Context, string, []string) ([]byte, error) { return []byte("not JSON"), nil }
	if _, err = creator.Schema(context.Background()); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	creator.run = func(context.Context, string, []string) ([]byte, error) { return nil, errors.New("old CLI") }
	if _, err = creator.Schema(context.Background()); err == nil || !strings.Contains(err.Error(), "update the CLI") {
		t.Fatal(err)
	}
}

// TestBoundedCreationOutputDrainsLargeWrites ensures a verbose child cannot block on the retention limit.
func TestBoundedCreationOutputDrainsLargeWrites(t *testing.T) {
	output := &boundedOutput{}
	data := make([]byte, 2*1024*1024)
	n, err := output.Write(data)
	if n != len(data) || err != nil || output.Len() != 1024*1024 {
		t.Fatalf("n=%d err=%v retained=%d", n, err, output.Len())
	}
	n, err = output.Write([]byte("more"))
	if n != 4 || err != nil || output.Len() != 1024*1024 {
		t.Fatal("retention limit drifted")
	}
}

// TestSchemaRemainsStackNeutral uses neither GoForj names nor its module requirement.
func TestSchemaRemainsStackNeutral(t *testing.T) {
	schema := Schema{SchemaVersion: 1, ProviderID: "other", Fields: []Field{{Key: "output-dir", Role: "directory", Label: "Output", Step: "Project", Kind: "path"}}}
	request := Request{ProviderID: "other", SchemaVersion: 1, Options: map[string]string{"output-dir": filepath.Join(t.TempDir(), "site")}}
	if _, err := schema.Directory(request); err != nil {
		t.Fatal(err)
	}
	request.ProviderID = "wrong"
	if _, err := schema.Directory(request); err == nil {
		t.Fatal("wrong provider accepted")
	}
	request.ProviderID = "other"
	request.SchemaVersion = 2
	if _, err := schema.Directory(request); err == nil {
		t.Fatal("wrong version accepted")
	}
	schema.Fields[0].Role = ""
	request.SchemaVersion = 1
	if _, err := schema.Directory(request); err == nil {
		t.Fatal("missing destination accepted")
	}
}

// TestSchemaConditionsAreValidated ensures malformed visibility rules cannot silently hide required fields.
func TestSchemaConditionsAreValidated(t *testing.T) {
	for _, field := range []Field{
		{Key: "extra", Label: "Extra", Step: "Options", Kind: "text", Requires: []string{"ui"}, DependsOn: "unknown"},
		{Key: "extra", Label: "Extra", Step: "Options", Kind: "text", When: map[string]string{"unknown": "custom"}},
		{Key: "extra", Label: "Extra", Step: "Options", Kind: "text", When: map[string]string{"extra": "custom"}},
	} {
		schema := testSchema()
		schema.Fields = append(schema.Fields, field)
		if schema.Validate() == nil {
			t.Fatal("invalid condition accepted")
		}
	}
	field := Field{When: map[string]string{"preset": "custom"}, DependsOn: "features", Requires: []string{"ui"}}
	if field.Available(map[string]string{"preset": "default", "features": "ui"}) || field.Available(map[string]string{"preset": "custom"}) || !field.Available(map[string]string{"preset": "custom", "features": "ui"}) {
		t.Fatal("visibility conditions ignored")
	}
}
