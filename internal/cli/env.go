package cli

import (
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// claudePlanFor computes the environment plan for a claude provider.
func claudePlanFor(name string, p config.Provider, variant, key string) provider.EnvPlan {
	return provider.ClaudeEnv(name, p, variant, key)
}

// codexPlanFor computes the environment plan for a codex provider.
func codexPlanFor(name string, p config.Provider, key string) provider.EnvPlan {
	return provider.CodexEnv(name, p, key)
}

// envPlanViewFrom converts an EnvPlan into masked display lines.
// Secrets show only their first and last few characters, so a full key is
// never exposed in terminal output or screenshots.
func envPlanViewFrom(plan provider.EnvPlan) envPlanView {
	out := make(envPlanView, 0, len(plan.Set)+len(plan.Unset))
	for _, kv := range plan.Set {
		out = append(out, "export "+kv.Key+"="+maskIfSensitive(kv.Key, kv.Value))
	}
	for _, k := range plan.Unset {
		out = append(out, "unset "+k)
	}
	return out
}

// sensitiveMarkers match environment variable names that hold secrets.
var sensitiveMarkers = []string{"KEY", "TOKEN", "SECRET", "PASSWORD"}

func isSensitiveKey(k string) bool {
	up := strings.ToUpper(k)
	for _, m := range sensitiveMarkers {
		if strings.Contains(up, m) {
			return true
		}
	}
	return false
}

// maskIfSensitive masks a secret value, keeping the first and last 4
// characters so the entry is still recognizable.
func maskIfSensitive(k, v string) string {
	if !isSensitiveKey(k) || len(v) <= 8 {
		return v
	}
	return v[:4] + strings.Repeat("*", 8) + v[len(v)-4:]
}
