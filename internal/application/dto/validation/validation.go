package validation

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/matheusj989/wagering-wallet-service/internal/domain/wagering"
)

const MaxOpaqueLength = 255

var engine = newEngine()

func newEngine() *validator.Validate {
	instance := validator.New(validator.WithRequiredStructEnabled())

	instance.RegisterTagNameFunc(func(field reflect.StructField) string {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return field.Name
		}
		return name
	})

	rules := map[string]validator.Func{
		"opaque": func(field validator.FieldLevel) bool {
			return isOpaque(field.Field().String())
		},
		"wager_kind": func(field validator.FieldLevel) bool {
			return wagering.Kind(field.Field().String()).External()
		},
	}
	for name, rule := range rules {
		if err := instance.RegisterValidation(name, rule); err != nil {
			panic(fmt.Sprintf("validation: the %s rule could not be registered: %v", name, err))
		}
	}
	return instance
}

func Struct(value any) error {
	return engine.Struct(value)
}

func isOpaque(value string) bool {
	if value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// Identifier parses a UUID coming from the wire and records the field when the
// value cannot be one, so both edges report the same message for the same mistake.
func Identifier(problems *Error, field string, value string) uuid.UUID {
	if value == "" {
		problems.Add(field, "is required")
		return uuid.Nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		problems.Add(field, "must be a UUID")
		return uuid.Nil
	}
	return parsed
}
