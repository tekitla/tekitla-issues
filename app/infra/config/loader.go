package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config representa la configuración de issues-service.
// Todo sale de variables de entorno; en desarrollo se cargan de .env.dev.
type Config struct {
	// Entorno
	AppEnv     string
	ServiceEnv string
	Debug      bool
	Testing    bool

	// gRPC
	GRPCPort int

	// Base de datos
	DatabaseURL string
	DBSchema    string

	// Logging
	LogLevel string

	// SessionStallAfter: una sesión de agente que trabaja y no reporta hace
	// más pasa a «colgado» (issue 026). SESSION_STALL_AFTER, duración de Go
	// ("10m"); vacía o inválida = 10 minutos.
	SessionStallAfter time.Duration

	// GitHub App del ambiente (issue 016). Todas vacías: la integración con
	// GitHub está apagada. Las carga Victor en Coolify; nunca van al repo ni
	// a los logs (el banner no las muestra).
	GitHubAppID         string
	GitHubAppSlug       string
	GitHubAppPrivateKey string // PEM en base64 (o el PEM tal cual)
	GitHubWebhookSecret string

	// OpenTelemetry (se exponen para el banner/logs; el exporter se cablea
	// cuando el servicio lo necesite)
	OtelExporterEndpoint string
	OtelServiceName      string
	OtelResourceAttrs    string
}

// Load carga la configuración desde variables de entorno.
//
// ENV_FILE permite indicar la ruta del archivo de entorno (útil si el binario
// corre desde un directorio temporal, ej. el IDE). Si no está y APP_ENV es
// development (o vacío), se busca .env.dev subiendo desde el CWD.
func Load() *Config {
	if envFile := os.Getenv("ENV_FILE"); envFile != "" {
		_ = godotenv.Overload(envFile)
	} else if os.Getenv("APP_ENV") == "" || os.Getenv("APP_ENV") == "development" {
		if f := findEnvFile(".env.dev"); f != "" {
			_ = godotenv.Overload(f)
		}
	}

	return &Config{
		AppEnv:     getEnv("APP_ENV", "development"),
		ServiceEnv: getEnv("SERVICE_ENV", "dev"),
		Debug:      getEnvAsBool("DEBUG", true),
		Testing:    getEnvAsBool("TESTING", false),

		GRPCPort: getEnvAsInt("GRPC_PORT", 5000),

		DatabaseURL: getEnv("DATABASE_URL", ""),
		DBSchema:    "issues",

		LogLevel: getEnv("LOG_LEVEL", "info"),

		SessionStallAfter: getEnvAsDuration("SESSION_STALL_AFTER", 10*time.Minute),

		GitHubAppID:         getEnv("GITHUB_APP_ID", ""),
		GitHubAppSlug:       getEnv("GITHUB_APP_SLUG", ""),
		GitHubAppPrivateKey: getEnv("GITHUB_APP_PRIVATE_KEY", ""),
		GitHubWebhookSecret: getEnv("GITHUB_WEBHOOK_SECRET", ""),

		OtelExporterEndpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		OtelServiceName:      getEnv("OTEL_SERVICE_NAME", "issues-service"),
		OtelResourceAttrs: getEnv(
			"OTEL_RESOURCE_ATTRIBUTES",
			"service.version=dev,deployment.environment=dev",
		),
	}
}

// findEnvFile busca name subiendo directorios desde el CWD. "" si no existe.
func findEnvFile(name string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvAsBool(key string, fallback bool) bool {
	if value, ok := os.LookupEnv(key); ok {
		switch strings.ToLower(value) {
		case "true", "1", "yes", "y":
			return true
		case "false", "0", "no", "n":
			return false
		}
	}
	return fallback
}

func getEnvAsDuration(key string, fallback time.Duration) time.Duration {
	if value, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}
