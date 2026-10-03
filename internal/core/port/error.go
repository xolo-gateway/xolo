package port

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrCanceled        = errors.New("canceled")
	ErrAlreadyExists   = errors.New("already exists")
	ErrNotAllowed      = errors.New("not allowed")
	ErrLastOwner       = fmt.Errorf("%w: last owner", ErrNotAllowed)
	ErrParentNotFound  = errors.New("parent not found")
	ErrInvalidHostname = fmt.Errorf("%w: invalid hostname", ErrNotAllowed)

	// ErrInvalid signals a syntactically well-formed value that the domain
	// refuses: an unknown permission code, a role belonging to another
	// organization, a malformed slug...
	ErrInvalid = errors.New("invalid")
)
