package context

import "context"

const keyEnrollmentEnabled contextKey = "enrollmentEnabled"

// EnrollmentEnabled reports whether enrollment rules apply on this instance:
// they do not when members or memberships belong to the control plane.
func EnrollmentEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(keyEnrollmentEnabled).(bool)
	return enabled
}

func SetEnrollmentEnabled(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, keyEnrollmentEnabled, enabled)
}
