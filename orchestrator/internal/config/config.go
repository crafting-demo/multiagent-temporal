// Package config reads the Temporal connection settings the orchestrator
// sandbox provides as environment variables.
package config

import (
	"os"
	"strings"
)

type Config struct {
	Address   string
	Namespace string
	TaskQueue string
}

func FromEnv() Config {
	return Config{
		Address:   env("TEMPORAL_ADDRESS", "localhost:7233"),
		Namespace: env("TEMPORAL_NAMESPACE", "default"),
		TaskQueue: env("TEMPORAL_TASK_QUEUE", "deliver-change"),
	}
}

// UIURL returns the Temporal UI link for a workflow when running inside the
// orchestrator sandbox, whose temporal-ui endpoint follows Crafting's
// endpoint naming. It returns "" elsewhere.
func UIURL(namespace, workflowID string) string {
	suffix := os.Getenv("SANDBOX_ENDPOINT_DNS_SUFFIX")
	if suffix == "" {
		return ""
	}
	u := "https://temporal-ui" + suffix
	if workflowID != "" {
		u += "/namespaces/" + namespace + "/workflows/" + workflowID
	}
	return u
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
