package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxIntentionTokenRunes bounds each part of the intention triple: long
// enough for operator-chosen names, short enough to stay out of log and
// index abuse.
const maxIntentionTokenRunes = 128

// IntentionKey names one business intention of one actor running one
// operation. The triple (key, actor, operation) settles at most once: a
// retry resolves the stored response, and a different payload under the
// same triple is a conflict.
type IntentionKey string

// IntentionActor names who runs the intention.
type IntentionActor string

// IntentionOperation names which operation the intention runs.
type IntentionOperation string

// checkToken validates one part of the triple: non-blank, bounded and
// free of control characters, so keys stay comparable byte for byte.
func checkToken(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return ErrInvalidIntention
	}
	if utf8.RuneCountInString(raw) > maxIntentionTokenRunes {
		return ErrInvalidIntention
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ErrInvalidIntention
		}
	}
	return nil
}

// ParseIntentionKey validates the intention name.
func ParseIntentionKey(raw string) (IntentionKey, error) {
	if err := checkToken(raw); err != nil {
		return "", err
	}
	return IntentionKey(raw), nil
}

// ParseIntentionActor validates the actor name.
func ParseIntentionActor(raw string) (IntentionActor, error) {
	if err := checkToken(raw); err != nil {
		return "", err
	}
	return IntentionActor(raw), nil
}

// ParseIntentionOperation validates the operation name.
func ParseIntentionOperation(raw string) (IntentionOperation, error) {
	if err := checkToken(raw); err != nil {
		return "", err
	}
	return IntentionOperation(raw), nil
}
