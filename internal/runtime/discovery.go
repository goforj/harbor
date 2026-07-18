package runtime

import (
	"sort"
	"strings"

	"github.com/goforj/env/v2"
	"github.com/goforj/str/v2"
)

// PrimitiveInstance describes a configured primitive instance for app metadata and tooling.
type PrimitiveInstance struct {
	Name      string `json:"name"`
	Driver    string `json:"driver"`
	IsDefault bool   `json:"is_default"`
}

// DiscoverDatabaseInstances returns configured database connections with normalized driver names.
func DiscoverDatabaseInstances() []PrimitiveInstance {
	names := aboutDatabaseNames()
	defaultName := aboutDatabaseDefaultName()
	out := make([]PrimitiveInstance, 0, len(names))
	for _, name := range names {
		out = append(out, PrimitiveInstance{
			Name:      name,
			Driver:    NormalizeDBDriver(aboutDatabaseEnv(name, "DRIVER")),
			IsDefault: name == defaultName,
		})
	}
	return out
}

func NormalizeDBDriver(v string) string {
	driver := str.Of(v).ToLower().Trim().String()
	switch {
	case driver == "", driver == "-", driver == "db", driver == "nil", driver == "<nil>":
		return ""
	case driver == "postgresql" || driver == "pgx" || strings.Contains(driver, "postgres"):
		return "postgres"
	case driver == "sqlite3" || strings.Contains(driver, "sqlite"):
		return "sqlite"
	case driver == "mariadb" || strings.Contains(driver, "mysql"):
		return "mysql"
	default:
		return driver
	}
}

// aboutScopedNames uses the shared scope parser so multiword root keys cannot truncate child names.
func aboutScopedNames(prefix string, rootKeys []string) []string {
	scope := env.WithPrefix(prefix)
	names := []string{"default"}
	children := scope.ChildNames(rootKeys)
	normalized := make([]string, 0, len(children))
	for _, child := range children {
		name := str.Of(child).Trim().ToLower().String()
		if name == "" {
			continue
		}
		childScope := scope.Child(str.Of(name).Snake().ToUpper().String())
		if !aboutPrimitiveChildDefined(prefix, childScope) {
			continue
		}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return append(names, normalized...)
}

// aboutDatabaseNames excludes driver helper namespaces that resemble configured database children.
func aboutDatabaseNames() []string {
	scope := env.WithPrefix("DB")
	children := scope.ChildNames(aboutDatabaseRootKeys)
	names := []string{"default"}
	normalized := make([]string, 0, len(children))
	for _, child := range children {
		name := str.Of(child).Trim().ToLower().String()
		if name == "" || aboutDatabaseHelperName(name) {
			continue
		}
		childScope := scope.Child(str.Of(name).Snake().ToUpper().String())
		if !aboutDatabaseChildDefined(childScope) {
			continue
		}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return append(names, normalized...)
}

func aboutDatabaseHelperName(name string) bool {
	switch str.Of(name).ToLower().Trim().String() {
	case "mysql", "postgres", "postgresql", "sqlite", "sqlite3":
		return true
	default:
		return false
	}
}

func aboutDatabaseDefaultName() string {
	if value := str.Of(env.Get("DB_DEFAULT", "")).Trim().ToLower().String(); value != "" {
		return value
	}
	return "default"
}

func aboutDatabaseEnv(name, suffix string) string {
	scope := env.WithPrefix("DB")
	if name == "" || name == "default" {
		return scope.Get(suffix, "")
	}
	child := scope.Child(str.Of(name).Snake().ToUpper().String())
	if value := child.Get(suffix, ""); value != "" {
		return value
	}
	return scope.Get(suffix, "")
}

func aboutPrimitiveScope(prefix, name string) env.Scope {
	scope := env.WithPrefix(prefix)
	if name == "" || name == "default" {
		return scope
	}
	return scope.Child(str.Of(name).Snake().ToUpper().String())
}
