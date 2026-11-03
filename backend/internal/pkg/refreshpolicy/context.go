// Package refreshpolicy carries the strict maintenance token-request policy
// across service, repository and provider clients without an import cycle.
package refreshpolicy

import "context"

type singleAttemptKey struct{}

// WithSingleAttempt excludes retries, redirects and enrichment after rotation.
// It is not a substitute for durable pending/ambiguous outcome coordination.
func WithSingleAttempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, singleAttemptKey{}, true)
}

func SingleAttempt(ctx context.Context) bool {
	value, _ := ctx.Value(singleAttemptKey{}).(bool)
	return value
}
