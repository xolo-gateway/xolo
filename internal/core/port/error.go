package port

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrCanceled      = errors.New("canceled")
	ErrAlreadyExists = errors.New("already exists")
	ErrNotAllowed    = errors.New("not allowed")

	// ErrInvalid signals a syntactically well-formed value that the domain
	// refuses: an unknown permission code, a role belonging to another
	// organization, a malformed slug...
	ErrInvalid = errors.New("invalid")

	// ErrParentNotFound signals that the tenant or organization a resource
	// hangs from does not exist in the requested scope.
	ErrParentNotFound = errors.New("parent not found")

	// ErrLastOwner refuses a change that would leave a tenant or an
	// organization without an active owner.
	ErrLastOwner = fmt.Errorf("last owner: %w", ErrNotAllowed)

	// ErrInvalidHostname refuses a malformed hostname.
	ErrInvalidHostname = fmt.Errorf("invalid hostname: %w", ErrNotAllowed)

	// ErrPlatformAdminProtected refuses any provisioning change to a platform
	// administrator: provisioning never acts on platform-wide privileges.
	ErrPlatformAdminProtected = fmt.Errorf("platform admin protected: %w", ErrNotAllowed)
)
