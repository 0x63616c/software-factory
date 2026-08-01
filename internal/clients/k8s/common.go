package k8s

import (
	"context"
	"log/slog"
	"sort"

	corev1 "k8s.io/api/core/v1"
)

// Labels shared by the target Run Worker pod and its controller.
const (
	labelName       = "app.kubernetes.io/name"
	labelManagedBy  = "app.kubernetes.io/managed-by"
	labelTicket     = "software-factory.worldwidewebb.co/ticket"
	labelRunID      = "software-factory.worldwidewebb.co/run-id"
	labelGeneration = "software-factory.worldwidewebb.co/generation"

	labelManagedByValue = "software-factory"
	workVolumeName      = "work"
	workSizeLimitBytes  = 20 << 30
)

func sortedEnv(env map[string]string) []corev1.EnvVar {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]corev1.EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, corev1.EnvVar{Name: key, Value: env[key]})
	}
	return result
}

func ptr[T any](value T) *T { return &value }

type warningLogger struct{ logger *slog.Logger }

func (w warningLogger) HandleWarningHeaderWithContext(ctx context.Context, code int, agent, message string) {
	if code == 299 && message != "" {
		w.logger.WarnContext(ctx, "kubernetes apiserver warning", "agent", agent, "warning", message)
	}
}
