package recipe

import (
	"errors"
	"fmt"
)

// ErrInvalid distinguishes user input from storage or execution failures.
var ErrInvalid = errors.New("invalid recipe or configuration input")

func Invalid(err error) error { return fmt.Errorf("%w: %v", ErrInvalid, err) }
