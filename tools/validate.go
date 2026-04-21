package tools

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

var (
	namePattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	emailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("name must be 1-128 chars, alphanumeric with ._- allowed, must start with alphanumeric")
	}
	return nil
}

func ValidateEmail(email string) error {
	if !emailPattern.MatchString(email) || utf8.RuneCountInString(email) > 254 {
		return fmt.Errorf("invalid email format")
	}
	return nil
}

func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if utf8.RuneCountInString(password) > 128 {
		return fmt.Errorf("password must be at most 128 characters")
	}
	return nil
}

const MaxYAMLSize = 1 * 1024 * 1024 // 1MB

func ValidateYAMLSize(yaml string) error {
	if len(yaml) > MaxYAMLSize {
		return fmt.Errorf("YAML content exceeds maximum size of 1MB")
	}
	return nil
}
