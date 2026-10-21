package Service

import (
	"fmt"
	"regexp"
)

// ValidationService handles input validation and error handling
type ValidationService struct {
	// Validation rules
	maxMessageSize int
	maxKeyLength   int
	maxValueLength int
	allowedStages  map[string]bool
	peerIDRegex    *regexp.Regexp
}

// ValidationError represents a validation error with context
type ValidationError struct {
	Field   string
	Value   interface{}
	Message string
	Code    string
}

func (ve *ValidationError) Error() string {
	return fmt.Sprintf("validation error [%s] in field '%s': %s", ve.Code, ve.Field, ve.Message)
}

// NewValidationService creates a new validation service with default rules
func NewValidationService() *ValidationService {
	peerIDRegex, _ := regexp.Compile(`^[A-Za-z0-9+/=]{44,88}$`) // Base64 encoded peer ID pattern

	return &ValidationService{
		maxMessageSize: 1024 * 1024, // 1MB
		maxKeyLength:   256,
		maxValueLength: 1024 * 1024, // 1MB
		allowedStages: map[string]bool{
			"Type_AskForSubscription": true,
			"Type_VerifySubscription": true,
			"Type_EndPubSub":          true,
			"Type_Publish":            true,
			"Type_StartPubSub":        true,
			"Type_SubmitVote":         true,
		},
		peerIDRegex: peerIDRegex,
	}
}
