package projectdiscovery

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const managedHTTPRuntimePath = "internal/http/runtime.go"

// RenderUpdateRequiredError reports generated runtime behavior that cannot honor Harbor's assigned listener.
type RenderUpdateRequiredError struct{}

// Error explains the corrective render without treating editable version metadata as authoritative.
func (*RenderUpdateRequiredError) Error() string {
	return "the generated HTTP runtime does not preserve Harbor's assigned listener; run forj render with a current GoForj build"
}

// validateManagedHTTPRuntimeContract proves the behavior-bearing generated runtime inherits sparse aggregate-run defaults.
func validateManagedHTTPRuntimeContract(root string) error {
	filename := filepath.Join(root, filepath.FromSlash(managedHTTPRuntimePath))
	source, err := readManagedHTTPRuntimeSource(filename)
	if err != nil {
		return err
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, source, 0)
	if err != nil {
		return invalidProjectError(fmt.Errorf("parse generated HTTP runtime contract: %w", err))
	}

	call, found := managedRuntimeInheritanceCall(parsed)
	if !found || !managedRuntimeInheritanceHelper(parsed, call) {
		return invalidProjectError(&RenderUpdateRequiredError{})
	}
	return nil
}

// managedRuntimeCall identifies the generated helper and parameter names that carry listener defaults.
type managedRuntimeCall struct {
	helperName string
}

// managedRuntimeInheritanceCall finds the required default inheritance before normalization in RunWithConfig.
func managedRuntimeInheritanceCall(file *ast.File) (managedRuntimeCall, bool) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "RunWithConfig" || function.Body == nil || !runtimeMethodReceiver(function.Recv) {
			continue
		}
		receiverName, ok := singleFieldName(function.Recv)
		if !ok {
			continue
		}
		configName, ok := namedRuntimeConfigParameter(function.Type.Params)
		if !ok {
			continue
		}
		for _, statement := range function.Body.List {
			assignment, ok := statement.(*ast.AssignStmt)
			if !ok || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 || !identNamed(assignment.Lhs[0], configName) {
				continue
			}
			call, ok := assignment.Rhs[0].(*ast.CallExpr)
			if !ok || len(call.Args) != 2 || !identNamed(call.Args[0], configName) {
				continue
			}
			helper, ok := call.Fun.(*ast.Ident)
			if !ok || !selectorNamed(call.Args[1], receiverName, "config") {
				continue
			}
			return managedRuntimeCall{
				helperName: helper.Name,
			}, true
		}
	}
	return managedRuntimeCall{}, false
}

// managedRuntimeInheritanceHelper verifies both listener fields inherit defaults before canonical normalization.
func managedRuntimeInheritanceHelper(file *ast.File, call managedRuntimeCall) bool {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name != call.helperName || function.Body == nil {
			continue
		}
		parameters, ok := twoRuntimeConfigParameters(function.Type.Params)
		if !ok {
			return false
		}
		configName := parameters[0]
		defaultsName := parameters[1]
		return hasConditionalRuntimeDefault(function.Body, configName, defaultsName, "Host") &&
			hasConditionalRuntimeDefault(function.Body, configName, defaultsName, "Port") &&
			returnsNormalizedRuntimeConfig(function.Body, configName)
	}
	return false
}

// hasConditionalRuntimeDefault requires an empty-field guard around the exact corresponding default assignment.
func hasConditionalRuntimeDefault(body *ast.BlockStmt, configName string, defaultsName string, field string) bool {
	for _, statement := range body.List {
		condition, ok := statement.(*ast.IfStmt)
		if !ok || !conditionReferencesEmptyRuntimeField(condition.Cond, configName, field) {
			continue
		}
		for _, guarded := range condition.Body.List {
			assignment, ok := guarded.(*ast.AssignStmt)
			if !ok || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
				continue
			}
			if selectorNamed(assignment.Lhs[0], configName, field) && selectorNamed(assignment.Rhs[0], defaultsName, field) {
				return true
			}
		}
	}
	return false
}

// conditionReferencesEmptyRuntimeField accepts direct and trimmed empty checks without depending on formatting.
func conditionReferencesEmptyRuntimeField(expression ast.Expr, configName string, field string) bool {
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok || binary.Op != token.EQL {
		return false
	}
	if !emptyStringLiteral(binary.X) && !emptyStringLiteral(binary.Y) {
		return false
	}
	other := binary.X
	if emptyStringLiteral(binary.X) {
		other = binary.Y
	}
	found := false
	ast.Inspect(other, func(node ast.Node) bool {
		if selectorNamedNode(node, configName, field) {
			found = true
			return false
		}
		return true
	})
	return found
}

// returnsNormalizedRuntimeConfig requires the helper to retain the generated canonicalization boundary.
func returnsNormalizedRuntimeConfig(body *ast.BlockStmt, configName string) bool {
	for _, statement := range body.List {
		returned, ok := statement.(*ast.ReturnStmt)
		if !ok || len(returned.Results) != 1 {
			continue
		}
		call, ok := returned.Results[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 1 || !identNamed(call.Args[0], configName) {
			continue
		}
		if function, ok := call.Fun.(*ast.Ident); ok && function.Name == "normalizeRuntimeConfig" {
			return true
		}
	}
	return false
}

// readManagedHTTPRuntimeSource applies the existing bounded, same-file metadata reader to generated source.
func readManagedHTTPRuntimeSource(filename string) ([]byte, error) {
	if _, err := os.Stat(filename); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, invalidProjectError(&RenderUpdateRequiredError{})
		}
		return nil, fmt.Errorf("inspect generated HTTP runtime contract: %w", err)
	}
	var source strings.Builder
	if err := scanMetadataLines(filename, func(line string) (bool, error) {
		source.WriteString(line)
		source.WriteByte('\n')
		return false, nil
	}); err != nil {
		return nil, err
	}
	return []byte(source.String()), nil
}

// namedRuntimeConfigParameter finds the explicit RuntimeConfig value accepted by RunWithConfig.
func namedRuntimeConfigParameter(fields *ast.FieldList) (string, bool) {
	if fields == nil {
		return "", false
	}
	for _, field := range fields.List {
		identifier, ok := field.Type.(*ast.Ident)
		if !ok || identifier.Name != "RuntimeConfig" || len(field.Names) != 1 {
			continue
		}
		return field.Names[0].Name, true
	}
	return "", false
}

// singleFieldName returns the only explicit name in one receiver field list.
func singleFieldName(fields *ast.FieldList) (string, bool) {
	if fields == nil || len(fields.List) != 1 || len(fields.List[0].Names) != 1 {
		return "", false
	}
	return fields.List[0].Names[0].Name, true
}

// twoRuntimeConfigParameters accepts either separate or grouped names for exactly two RuntimeConfig values.
func twoRuntimeConfigParameters(fields *ast.FieldList) ([2]string, bool) {
	var result [2]string
	if fields == nil {
		return result, false
	}
	names := make([]string, 0, 2)
	for _, field := range fields.List {
		identifier, ok := field.Type.(*ast.Ident)
		if !ok || identifier.Name != "RuntimeConfig" {
			return result, false
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	if len(names) != 2 {
		return result, false
	}
	return [2]string{names[0], names[1]}, true
}

// runtimeMethodReceiver restricts the contract search to methods on the generated Runtime type.
func runtimeMethodReceiver(fields *ast.FieldList) bool {
	if fields == nil || len(fields.List) != 1 {
		return false
	}
	pointer, ok := fields.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	identifier, ok := pointer.X.(*ast.Ident)
	return ok && identifier.Name == "Runtime"
}

// identNamed reports whether expression is one exact identifier.
func identNamed(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

// selectorNamed reports whether expression is one direct named-field selection.
func selectorNamed(expression ast.Expr, owner string, field string) bool {
	return selectorNamedNode(expression, owner, field)
}

// selectorNamedNode lets AST inspection reuse the same exact selector check.
func selectorNamedNode(node ast.Node, owner string, field string) bool {
	selector, ok := node.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == field && identNamed(selector.X, owner)
}

// emptyStringLiteral recognizes only the canonical empty string constant.
func emptyStringLiteral(expression ast.Expr) bool {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && value == ""
}
