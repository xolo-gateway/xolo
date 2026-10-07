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

	// ErrInvalidCursor refuses a cursor that was altered, issued for another
	// collection, limit or feed, or that points past the feed.
	ErrInvalidCursor = errors.New("invalid cursor")

	// ErrCursorExpired signals a cursor older than its lifetime or than the
	// retained events: the consumer must rebuild from an inventory.
	ErrCursorExpired = errors.New("cursor expired")

	// ErrInvalidPrecondition refuses a malformed or unsupported precondition.
	ErrInvalidPrecondition = errors.New("invalid precondition")

	// ErrPreconditionFailed signals an If-Match that does not designate the
	// current revision of the resource.
	ErrPreconditionFailed = errors.New("precondition failed")
)

var (
	// ErrWebhookLeaseLost signals a delivery result recorded after its lease
	// expired, or after the subscription was reset or deleted.
	ErrWebhookLeaseLost = errors.New("webhook lease lost")

	// ErrWebhookCapacity refuses a subscription beyond the tenant's limit.
	ErrWebhookCapacity = errors.New("webhook capacity reached")
)
