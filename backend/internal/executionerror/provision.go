// Package executionerror defines backend-neutral execution failure classes
// that services can inspect without importing a concrete backend adapter.
package executionerror

import "errors"

// ErrProvisionOutcomeUnknown means a workspace create may have succeeded, but
// the backend could not prove and persist the result. Retrying the same attempt
// could create a duplicate workspace; callers must advance to a fresh attempt.
var ErrProvisionOutcomeUnknown = errors.New("provision outcome unknown")
