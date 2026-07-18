package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/goforj/env/v2"
	"github.com/goforj/str/v2"
	"gopkg.in/yaml.v3"
)

var (
	aboutDatabaseRootKeys = []string{
		"DRIVER",
		"DSN",
		"HOST",
		"PORT",
		"DATABASE",
		"USERNAME",
		"PASSWORD",
		"QUERY_LOGGING",
		"MAX_IDLE_CONNECTIONS",
		"MAX_OPEN_CONNECTIONS",
		"CONN_MAX_LIFETIME_MINUTES",
	}
	aboutMailRootKeys = []string{
		"DRIVER",
		"FROM_ADDRESS",
		"FROM_NAME",
		"LOG_BODIES",
		"SMTP_HOST",
		"SMTP_PORT",
		"SMTP_USERNAME",
		"SMTP_PASSWORD",
		"SMTP_IDENTITY",
		"SMTP_FORCE_TLS",
		"RESEND_API_KEY",
		"RESEND_ENDPOINT",
		"POSTMARK_SERVER_TOKEN",
		"POSTMARK_ENDPOINT",
		"POSTMARK_MESSAGE_STREAM",
		"MAILGUN_DOMAIN",
		"MAILGUN_API_KEY",
		"MAILGUN_ENDPOINT",
		"SENDGRID_API_KEY",
		"SENDGRID_ENDPOINT",
		"SES_REGION",
		"SES_ACCESS_KEY_ID",
		"SES_SECRET_ACCESS_KEY",
		"SES_SESSION_TOKEN",
		"SES_ENDPOINT",
		"SES_CONFIGURATION_SET",
	}
)

type AboutService struct{}

type AboutField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type AboutSectionRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type AboutConnectionData struct {
	Name      string       `json:"name"`
	IsDefault bool         `json:"is_default"`
	Details   []AboutField `json:"details,omitempty"`
}

type AboutSectionData struct {
	Title       string                `json:"title"`
	Rows        []AboutSectionRow     `json:"rows,omitempty"`
	Connections []AboutConnectionData `json:"connections,omitempty"`
}

// AboutReport describes the generated App's enabled runtime and resource surface.
type AboutReport struct {
	App         string             `json:"app"`
	Environment AboutEnvironment   `json:"environment"`
	Build       AboutBuild         `json:"build"`
	Network     AboutNetwork       `json:"network"`
	Databases   []AboutDatabase    `json:"databases,omitempty"`
	Mails       []AboutMail        `json:"mails,omitempty"`
	Sections    []AboutSectionData `json:"sections"`
}

type AboutEnvironment struct {
	AppName       string `json:"app_name"`
	Module        string `json:"module"`
	Environment   string `json:"environment"`
	Debug         bool   `json:"debug"`
	GoVersion     string `json:"go_version"`
	GoForjVersion string `json:"goforj_version"`
}

type AboutBuild struct {
	Components     []string `json:"components"`
	WireGenerated  string   `json:"wire_generated"`
	FrontendDist   string   `json:"frontend_dist,omitempty"`
	MigrationsPath string   `json:"migrations_path,omitempty"`
}

type AboutNetwork struct {
	AppURL        string `json:"app_url,omitempty"`
	APIURL        string `json:"api_url,omitempty"`
	LighthouseURL string `json:"lighthouse_url,omitempty"`
}

type AboutDatabase struct {
	Name      string       `json:"name"`
	IsDefault bool         `json:"is_default"`
	Driver    string       `json:"driver"`
	Host      string       `json:"host,omitempty"`
	Port      string       `json:"port,omitempty"`
	Database  string       `json:"database,omitempty"`
	DSN       string       `json:"dsn,omitempty"`
	URL       string       `json:"url,omitempty"`
	Summary   string       `json:"summary"`
	Details   []AboutField `json:"details,omitempty"`
}

type AboutMail struct {
	Name      string       `json:"name"`
	IsDefault bool         `json:"is_default"`
	Driver    string       `json:"driver"`
	From      string       `json:"from,omitempty"`
	Endpoint  string       `json:"endpoint,omitempty"`
	Summary   string       `json:"summary"`
	Details   []AboutField `json:"details,omitempty"`
}

// NewAboutService constructs the runtime report service.
func NewAboutService() *AboutService {
	return &AboutService{}
}

// Build assembles a report from the capabilities enabled for the current App.
func (s *AboutService) Build() AboutReport {
	appComponents := CurrentApp().Components
	report := AboutReport{
		App: strings.TrimSpace(env.Get("APP_NAME", "app")),
		Environment: AboutEnvironment{
			AppName:       env.Get("APP_NAME", "app"),
			Module:        "github.com/goforj/harbor",
			Environment:   env.Get("APP_ENV", "local"),
			Debug:         env.Get("APP_DEBUG", "0") != "0",
			GoVersion:     goruntime.Version(),
			GoForjVersion: aboutForjVersion(),
		},
		Build: AboutBuild{
			Components:    aboutComponents(appComponents),
			WireGenerated: aboutPresence(filepath.Join("app", "wire", "wire_gen.go")),
		},
		Network: AboutNetwork{
			AppURL: env.Get("APP_URL", "http://localhost:3000"),
		},
	}
	if raw := strings.TrimSpace(env.Get("LIGHTHOUSE_URL", "")); raw != "" {
		if env.GetBool("LIGHTHOUSE_ENABLED", "true") {
			report.Network.LighthouseURL = raw
		} else {
			report.Network.LighthouseURL = "disabled"
		}
	}
	report.Sections = s.sections(report, appComponents)
	return report
}

// sections converts the runtime report into the stable presentation sections consumed by the command.
func (s *AboutService) sections(report AboutReport, appComponents AppComponents) []AboutSectionData {
	sections := []AboutSectionData{
		{
			Title: "Environment",
			Rows: []AboutSectionRow{
				{Key: "App", Value: report.Environment.AppName},
				{Key: "Module", Value: report.Environment.Module},
				{Key: "Environment", Value: report.Environment.Environment},
				{Key: "Debug", Value: aboutEnabled(report.Environment.Debug)},
				{Key: "Go", Value: report.Environment.GoVersion},
				{Key: "GoForj Version", Value: report.Environment.GoForjVersion},
			},
		},
		{
			Title: "Build",
			Rows:  aboutBuildRows(report.Build),
		},
	}

	if rows := aboutNetworkRows(report.Network); len(rows) > 0 {
		sections = append(sections, AboutSectionData{Title: "Network", Rows: rows})
	}

	return sections
}

func aboutForjVersion() string {
	data, err := os.ReadFile(".goforj.yml")
	if err != nil {
		return "-"
	}
	var cfg struct {
		Render struct {
			GoForjVersion string `yaml:"goforj_version"`
		} `yaml:"render"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "-"
	}
	value := strings.TrimSpace(cfg.Render.GoForjVersion)
	if value == "" {
		return "-"
	}
	return value
}

func aboutBuildRows(build AboutBuild) []AboutSectionRow {
	rows := []AboutSectionRow{
		{Key: "Components", Value: strings.Join(build.Components, ", ")},
		{Key: "Wire Generated", Value: build.WireGenerated},
	}
	if strings.TrimSpace(build.FrontendDist) != "" {
		rows = append(rows, AboutSectionRow{Key: "Frontend Dist", Value: build.FrontendDist})
	}
	if strings.TrimSpace(build.MigrationsPath) != "" {
		rows = append(rows, AboutSectionRow{Key: "Migrations Path", Value: build.MigrationsPath})
	}
	return rows
}

func aboutNetworkRows(network AboutNetwork) []AboutSectionRow {
	rows := []AboutSectionRow{}
	if strings.TrimSpace(network.AppURL) != "" {
		rows = append(rows, AboutSectionRow{Key: "App URL", Value: network.AppURL})
	}
	if strings.TrimSpace(network.APIURL) != "" {
		rows = append(rows, AboutSectionRow{Key: "API URL", Value: network.APIURL})
	}
	if strings.TrimSpace(network.LighthouseURL) != "" {
		rows = append(rows, AboutSectionRow{Key: "Lighthouse URL", Value: network.LighthouseURL})
	}
	return rows
}

func aboutDatabaseConnections(databases []AboutDatabase) []AboutConnectionData {
	connections := make([]AboutConnectionData, 0, len(databases))
	for _, database := range databases {
		connections = append(connections, AboutConnectionData{
			Name:      database.Name,
			IsDefault: database.IsDefault,
			Details:   database.Details,
		})
	}
	return connections
}

func aboutMailConnections(mails []AboutMail) []AboutConnectionData {
	connections := make([]AboutConnectionData, 0, len(mails))
	for _, mail := range mails {
		connections = append(connections, AboutConnectionData{
			Name:      mail.Name,
			IsDefault: mail.IsDefault,
			Details:   mail.Details,
		})
	}
	return connections
}

// aboutComponents reports only the components enabled for the current App projection.
func aboutComponents(appComponents AppComponents) []string {
	components := []string{}
	components = append(components, "lighthouse")
	return components
}

func aboutDatabaseReports() []AboutDatabase {
	instances := DiscoverDatabaseInstances()
	reports := make([]AboutDatabase, 0, len(instances))
	for _, instance := range instances {
		name := instance.Name
		dsn := aboutMaskDSN(aboutDatabaseEnv(name, "DSN"))
		if dsn == "-" {
			dsn = ""
		}
		host := ""
		port := ""
		database := ""
		url := ""
		switch instance.Driver {
		case "postgres", "postgresql", "mysql", "mariadb":
			host = strings.TrimSpace(aboutDatabaseEnv(name, "HOST"))
			port = strings.TrimSpace(aboutDatabaseEnv(name, "PORT"))
			database = strings.TrimSpace(aboutDatabaseEnv(name, "DATABASE"))
			if host != "" && port != "" && database != "" {
				url = host + ":" + port + "/" + database
			}
		}
		details := aboutDatabaseDetails(name)
		reports = append(reports, AboutDatabase{
			Name:      name,
			IsDefault: instance.IsDefault,
			Driver:    instance.Driver,
			Host:      host,
			Port:      port,
			Database:  database,
			DSN:       dsn,
			URL:       url,
			Summary:   aboutDetailSummary(details),
			Details:   details,
		})
	}
	return reports
}

func aboutMailReports() []AboutMail {
	names := aboutScopedNames("MAIL", aboutMailRootKeys)
	reports := make([]AboutMail, 0, len(names))
	for _, name := range names {
		scope := aboutPrimitiveScope("MAIL", name)
		driver := aboutMailDriver(scope.Get("DRIVER", "log"))
		if driver == "" {
			driver = "log"
		}
		details := aboutMailDetails(name)
		reports = append(reports, AboutMail{
			Name:      name,
			IsDefault: name == "default",
			Driver:    driver,
			From:      aboutFieldValue(details, "From Address", "From Name"),
			Endpoint:  aboutFieldValue(details, "Host", "Endpoint", "Domain", "Region"),
			Summary:   aboutDetailSummary(details),
			Details:   details,
		})
	}
	return reports
}

// aboutPrimitiveChildDefined distinguishes explicitly configured child resources from inherited defaults.
func aboutPrimitiveChildDefined(prefix string, scope env.Scope) bool {
	switch prefix {
	case "MAIL":
		return aboutAnySet(scope, "DRIVER", "FROM_ADDRESS", "FROM_NAME", "SMTP_HOST", "RESEND_ENDPOINT", "POSTMARK_ENDPOINT", "MAILGUN_DOMAIN", "SENDGRID_ENDPOINT", "SES_REGION")
	default:
		return false
	}
}

func aboutDatabaseChildDefined(scope env.Scope) bool {
	return aboutAnySet(scope, "DRIVER", "DSN", "DATABASE", "HOST")
}

func aboutAnySet(scope env.Scope, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(scope.Get(key, "")) != "" {
			return true
		}
	}
	return false
}

func aboutDatabaseDetails(name string) []AboutField {
	driver := NormalizeDBDriver(aboutDatabaseEnv(name, "DRIVER"))
	dsn := aboutMaskDSN(aboutDatabaseEnv(name, "DSN"))
	details := []AboutField{
		{Key: "Driver", Value: fallbackAboutValue(driver, "-")},
	}
	switch driver {
	case "postgres", "postgresql", "mysql", "mariadb":
		if host := strings.TrimSpace(aboutDatabaseEnv(name, "HOST")); host != "" {
			details = append(details, AboutField{Key: "Host", Value: host})
		}
		if port := strings.TrimSpace(aboutDatabaseEnv(name, "PORT")); port != "" {
			details = append(details, AboutField{Key: "Port", Value: port})
		}
		if database := strings.TrimSpace(aboutDatabaseEnv(name, "DATABASE")); database != "" {
			details = append(details, AboutField{Key: "Database", Value: database})
		}
		if dsn != "-" && len(details) == 1 {
			details = append(details, AboutField{Key: "DSN", Value: dsn})
		}
	case "sqlite":
		if dsn != "-" {
			details = append(details, AboutField{Key: "DSN", Value: dsn})
		}
	default:
		if dsn != "-" {
			details = append(details, AboutField{Key: "DSN", Value: dsn})
		}
	}
	details = aboutAppendIfTrue(details, "Query Logging", aboutDatabaseEnv(name, "QUERY_LOGGING"))
	details = aboutAppendIfSet(details, "Max Idle", aboutDatabaseEnv(name, "MAX_IDLE_CONNECTIONS"))
	details = aboutAppendIfSet(details, "Max Open", aboutDatabaseEnv(name, "MAX_OPEN_CONNECTIONS"))
	details = aboutAppendIfSet(details, "Conn Max Lifetime Minutes", aboutDatabaseEnv(name, "CONN_MAX_LIFETIME_MINUTES"))
	return details
}

func aboutMailDetails(name string) []AboutField {
	scope := aboutPrimitiveScope("MAIL", name)
	driver := aboutMailDriver(scope.Get("DRIVER", "log"))
	if driver == "" {
		driver = "log"
	}
	details := []AboutField{
		{Key: "Driver", Value: driver},
	}
	details = aboutAppendIfSet(details, "From Address", scope.Get("FROM_ADDRESS", ""))
	details = aboutAppendIfSet(details, "From Name", scope.Get("FROM_NAME", ""))
	switch driver {
	case "log":
		details = aboutAppendIfSet(details, "Log Bodies", aboutTruthyField(scope.Get("LOG_BODIES", "")))
	case "smtp":
		details = aboutAppendIfSet(details, "Host", scope.Get("SMTP_HOST", ""))
		details = aboutAppendIfSet(details, "Port", scope.Get("SMTP_PORT", ""))
		details = aboutAppendIfSet(details, "Username", scope.Get("SMTP_USERNAME", ""))
		details = aboutAppendIfSet(details, "Identity", scope.Get("SMTP_IDENTITY", ""))
		details = aboutAppendIfSet(details, "Force TLS", aboutTruthyField(scope.Get("SMTP_FORCE_TLS", "")))
	case "resend":
		details = aboutAppendIfSet(details, "Endpoint", scope.Get("RESEND_ENDPOINT", ""))
	case "postmark":
		details = aboutAppendIfSet(details, "Endpoint", scope.Get("POSTMARK_ENDPOINT", ""))
		details = aboutAppendIfSet(details, "Message Stream", scope.Get("POSTMARK_MESSAGE_STREAM", ""))
	case "mailgun":
		details = aboutAppendIfSet(details, "Domain", scope.Get("MAILGUN_DOMAIN", ""))
		details = aboutAppendIfSet(details, "Endpoint", scope.Get("MAILGUN_ENDPOINT", ""))
	case "sendgrid":
		details = aboutAppendIfSet(details, "Endpoint", scope.Get("SENDGRID_ENDPOINT", ""))
	case "ses":
		details = aboutAppendIfSet(details, "Region", scope.Get("SES_REGION", ""))
		details = aboutAppendIfSet(details, "Endpoint", scope.Get("SES_ENDPOINT", ""))
		details = aboutAppendIfSet(details, "Configuration Set", scope.Get("SES_CONFIGURATION_SET", ""))
	}
	return details
}

func aboutRedisSummary(scope env.Scope, prefix string) string {
	host := strings.TrimSpace(scope.Get("HOST", env.Get("REDIS_HOST", "")))
	if host == "" {
		host = strings.TrimSpace(scope.Get("ADDR", ""))
	}
	port := strings.TrimSpace(scope.Get("PORT", env.Get("REDIS_PORT", "")))
	db := strings.TrimSpace(scope.Get("DB", env.Get("REDIS_DB", "")))
	if addr := strings.TrimSpace(scope.Get("ADDR", "")); addr != "" && !strings.Contains(addr, ":") {
		host = addr
	}
	if addr := strings.TrimSpace(scope.Get("ADDR", "")); strings.Contains(addr, ":") {
		if db == "" {
			return "redis://" + addr
		}
		return "redis://" + addr + "/" + db
	}
	if host == "" {
		return strings.ToLower(prefix)
	}
	if port == "" {
		port = "6379"
	}
	if db == "" {
		db = "0"
	}
	return fmt.Sprintf("redis://%s:%s/%s", host, port, db)
}

func aboutMaskDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "-"
	}
	if strings.Contains(dsn, "@") && strings.Contains(dsn, ":") {
		parts := strings.SplitN(dsn, "@", 2)
		if len(parts) == 2 {
			creds := parts[0]
			if idx := strings.Index(creds, ":"); idx != -1 {
				return creds[:idx] + ":***@" + parts[1]
			}
		}
	}
	return dsn
}

func aboutMailDriver(v string) string {
	driver := str.Of(v).ToLower().Trim().String()
	switch {
	case driver == "", driver == "-", driver == "nil", driver == "<nil>":
		return ""
	case strings.Contains(driver, "smtp"):
		return "smtp"
	case strings.Contains(driver, "resend"):
		return "resend"
	case strings.Contains(driver, "postmark"):
		return "postmark"
	case strings.Contains(driver, "mailgun"):
		return "mailgun"
	case strings.Contains(driver, "sendgrid"):
		return "sendgrid"
	case strings.Contains(driver, "ses"):
		return "ses"
	case strings.Contains(driver, "log"):
		return "log"
	default:
		return driver
	}
}

func aboutPresence(path string) string {
	if _, err := os.Stat(path); err == nil {
		return "present"
	}
	return "missing"
}

func aboutEnabled(enabled bool) string {
	if enabled {
		return "true"
	}
	return "false"
}

func aboutAppendIfSet(details []AboutField, key string, value string) []AboutField {
	value = strings.TrimSpace(value)
	if value == "" {
		return details
	}
	return append(details, AboutField{Key: key, Value: value})
}

func aboutAppendIfNot(details []AboutField, key string, value string, defaultValue string) []AboutField {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, strings.TrimSpace(defaultValue)) {
		return details
	}
	return append(details, AboutField{Key: key, Value: value})
}

func aboutAppendIfTrue(details []AboutField, key string, value string) []AboutField {
	value = aboutTruthyField(value)
	if value == "" {
		return details
	}
	return append(details, AboutField{Key: key, Value: value})
}

func aboutTruthyField(value string) string {
	value = str.Of(value).ToLower().Trim().String()
	switch value {
	case "", "0", "false", "no", "off":
		return ""
	default:
		return "true"
	}
}

func aboutFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func aboutDetailSummary(details []AboutField) string {
	if len(details) == 0 {
		return "-"
	}
	driver := ""
	for _, detail := range details {
		if detail.Key == "Driver" {
			driver = detail.Value
			break
		}
	}
	parts := make([]string, 0, len(details)-1)
	for _, detail := range details {
		if detail.Key == "Driver" || strings.TrimSpace(detail.Value) == "" {
			continue
		}
		parts = append(parts, detail.Value)
	}
	if driver == "" {
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, " · ")
	}
	if len(parts) == 0 {
		return driver
	}
	return driver + " · " + strings.Join(parts, " · ")
}

func aboutFieldValue(details []AboutField, keys ...string) string {
	for _, key := range keys {
		for _, detail := range details {
			if detail.Key == key && strings.TrimSpace(detail.Value) != "" {
				return detail.Value
			}
		}
	}
	return ""
}

func fallbackAboutValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
