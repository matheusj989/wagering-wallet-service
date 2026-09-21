package validation

import (
	"errors"
	"fmt"

	"github.com/go-playground/validator/v10"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error carries every field the caller has to fix, not only the first one, so a
// bad request can be corrected in a single round trip.
type Error struct {
	Fields []FieldError
}

func Invalid(field string, message string) *Error {
	problems := &Error{}
	problems.Add(field, message)
	return problems
}

func (e *Error) Error() string {
	if len(e.Fields) == 0 {
		return "the request is not valid"
	}
	return fmt.Sprintf("%s %s", e.Fields[0].Field, e.Fields[0].Message)
}

func (e *Error) Add(field string, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Message: message})
}

func (e *Error) Collect(err error, rename map[string]string) {
	if err == nil {
		return
	}

	var failures validator.ValidationErrors
	if !errors.As(err, &failures) {
		e.Add("body", "could not be validated")
		return
	}
	for _, failure := range failures {
		field := failure.Field()
		if renamed, ok := rename[field]; ok {
			field = renamed
		}
		e.Add(field, explain(failure))
	}
}

func (e *Error) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

func explain(failure validator.FieldError) string {
	switch failure.Tag() {
	case "required":
		return "is required"
	case "max":
		return fmt.Sprintf("must not be longer than %s characters", failure.Param())
	case "gt":
		return fmt.Sprintf("must be greater than %s", failure.Param())
	case "gte":
		return fmt.Sprintf("must not be lower than %s", failure.Param())
	case "lte":
		return fmt.Sprintf("must not be greater than %s", failure.Param())
	case "uuid":
		return "must be a UUID"
	case "opaque":
		return "must not start or end with spaces, and must not contain control characters"
	case "wager_kind":
		return "must be one of BET, WIN, LOSS, REFUND or ROLLBACK"
	default:
		return fmt.Sprintf("does not satisfy %s", failure.Tag())
	}
}
