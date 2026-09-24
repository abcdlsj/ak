package cli

import (
	"strings"

	"github.com/abcdlsj/ak/internal/config"
	"github.com/abcdlsj/ak/internal/provider"
)

// claudePlanFor 计算 claude 供应商的环境方案。
func claudePlanFor(name string, p config.Provider, variant, key string) provider.EnvPlan {
	return provider.ClaudeEnv(name, p, variant, key)
}

// codexPlanFor 计算 codex 供应商的环境方案。
func codexPlanFor(name string, p config.Provider, key string) provider.EnvPlan {
	return provider.CodexEnv(name, p, key)
}

// envPlanViewFrom 把 EnvPlan 转成脱敏后的展示行。
// 密钥只显示前后各几位,避免在终端和截图里暴露完整 key。
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

// sensitiveMarkers 匹配密钥类变量名。
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

// maskIfSensitive 对密钥类值打码,保留前后各 4 位便于辨识。
func maskIfSensitive(k, v string) string {
	if !isSensitiveKey(k) || len(v) <= 8 {
		return v
	}
	return v[:4] + strings.Repeat("*", 8) + v[len(v)-4:]
}
