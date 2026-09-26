package cli

import (
	"fmt"
	"strings"

	"github.com/abcdlsj/ak/internal/provider"
)

// launchView renders what a provider's command does, with variant applied
// when non-empty. Secrets are masked.
func launchView(l provider.Launch, variant string) (envPlanView, error) {
	view := envPlanViewFrom(l.Env)
	args := l.Args
	if variant != "" {
		found := false
		for _, v := range l.Variants {
			if v.Name != variant {
				continue
			}
			found = true
			view = append(view, envPlanViewFrom(provider.EnvPlan{Set: v.Env})...)
			args = append(append([]string(nil), args...), v.Args...)
		}
		if !found {
			return nil, fmt.Errorf("unknown variant %q", variant)
		}
	}
	if len(args) > 0 {
		view = append(view, "args "+strings.Join(args, " "))
	}
	return view, nil
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
