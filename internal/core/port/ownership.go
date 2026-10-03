package port

import "fmt"

// ErrOwnershipDenied distinguishes startup write authority from domain conflicts.
var ErrOwnershipDenied = fmt.Errorf("%w: family belongs to another write authority", ErrNotAllowed)
