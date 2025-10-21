package Service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	log "gossipnode/AVC/BuddyNodes/MessagePassing/Logger"
	"gossipnode/AVC/BuddyNodes/Types"
	Struct "gossipnode/Pubsub/DataProcessing/Struct"

	"github.com/libp2p/go-libp2p/core/peer"
	"go.uber.org/zap"
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

// ValidateGossipMessage validates a gossip message
func (vs *ValidationService) ValidateGossipMessage(msg *Struct.GossipMessage) error {
	if msg == nil {
		return &ValidationError{
			Field:   "message",
			Value:   nil,
			Message: "message cannot be nil",
			Code:    "NULL_MESSAGE",
		}
	}

	// Validate message ID
	if err := vs.validateMessageID(msg.ID); err != nil {
		return err
	}

	// Validate sender
	if err := vs.validatePeerID(string(msg.Sender)); err != nil {
		return &ValidationError{
			Field:   "sender",
			Value:   msg.Sender,
			Message: err.Error(),
			Code:    "INVALID_SENDER",
		}
	}

	// Validate timestamp
	if err := vs.validateTimestamp(time.Unix(msg.Timestamp, 0)); err != nil {
		return err
	}

	// Validate data
	if err := vs.validateMessageData(msg.Data); err != nil {
		return err
	}

	return nil
}

// ValidateOperation validates a CRDT operation
func (vs *ValidationService) ValidateOperation(op *Types.OP) error {
	if op == nil {
		return &ValidationError{
			Field:   "operation",
			Value:   nil,
			Message: "operation cannot be nil",
			Code:    "NULL_OPERATION",
		}
	}

	// Validate operation type
	if err := vs.validateOperationType(op.OpType); err != nil {
		return err
	}

	// Validate node ID
	if err := vs.validatePeerID(op.NodeID.String()); err != nil {
		return &ValidationError{
			Field:   "node_id",
			Value:   op.NodeID.String(),
			Message: err.Error(),
			Code:    "INVALID_NODE_ID",
		}
	}

	// Validate key-value pair
	if err := vs.validateKeyValue(op.KeyValue); err != nil {
		return err
	}

	return nil
}

// ValidateMessageSize validates message size constraints
func (vs *ValidationService) ValidateMessageSize(message string) error {
	if len(message) > vs.maxMessageSize {
		return &ValidationError{
			Field:   "message_size",
			Value:   len(message),
			Message: fmt.Sprintf("message size %d exceeds maximum %d bytes", len(message), vs.maxMessageSize),
			Code:    "MESSAGE_TOO_LARGE",
		}
	}
	return nil
}

// validateMessageID validates message ID format
func (vs *ValidationService) validateMessageID(id string) error {
	if id == "" {
		return &ValidationError{
			Field:   "id",
			Value:   id,
			Message: "message ID cannot be empty",
			Code:    "EMPTY_ID",
		}
	}

	if len(id) > 64 {
		return &ValidationError{
			Field:   "id",
			Value:   id,
			Message: "message ID too long",
			Code:    "ID_TOO_LONG",
		}
	}

	return nil
}

// validatePeerID validates peer ID format
func (vs *ValidationService) validatePeerID(peerID string) error {
	if peerID == "" {
		return fmt.Errorf("peer ID cannot be empty")
	}

	// Try to parse as peer.ID
	if _, err := peer.Decode(peerID); err != nil {
		return fmt.Errorf("invalid peer ID format: %v", err)
	}

	return nil
}

// validateTimestamp validates message timestamp
func (vs *ValidationService) validateTimestamp(timestamp time.Time) error {
	now := time.Now()

	// Check if timestamp is too far in the past (older than 1 hour)
	if now.Sub(timestamp) > time.Hour {
		return &ValidationError{
			Field:   "timestamp",
			Value:   timestamp,
			Message: "message timestamp is too old",
			Code:    "TIMESTAMP_TOO_OLD",
		}
	}

	// Check if timestamp is in the future (more than 5 minutes)
	if timestamp.Sub(now) > 5*time.Minute {
		return &ValidationError{
			Field:   "timestamp",
			Value:   timestamp,
			Message: "message timestamp is in the future",
			Code:    "TIMESTAMP_IN_FUTURE",
		}
	}

	return nil
}

// validateMessageData validates message data structure
func (vs *ValidationService) validateMessageData(data *Struct.Message) error {
	if data == nil {
		return &ValidationError{
			Field:   "data",
			Value:   nil,
			Message: "message data cannot be nil",
			Code:    "NULL_DATA",
		}
	}

	// Validate ACK stage
	if err := vs.validateACKStage(data.ACK.Stage); err != nil {
		return err
	}

	// Validate message content
	if err := vs.ValidateMessageSize(data.Message); err != nil {
		return err
	}

	return nil
}

// validateACKStage validates ACK stage values
func (vs *ValidationService) validateACKStage(stage string) error {
	if stage == "" {
		return &ValidationError{
			Field:   "ack_stage",
			Value:   stage,
			Message: "ACK stage cannot be empty",
			Code:    "EMPTY_STAGE",
		}
	}

	if !vs.allowedStages[stage] {
		return &ValidationError{
			Field:   "ack_stage",
			Value:   stage,
			Message: fmt.Sprintf("invalid ACK stage: %s", stage),
			Code:    "INVALID_STAGE",
		}
	}

	return nil
}

// validateOperationType validates operation type
func (vs *ValidationService) validateOperationType(opType int8) error {
	validTypes := map[int8]bool{
		Types.ADD:        true,
		Types.REMOVE:     true,
		Types.SYNC:       true,
		Types.COUNTERINC: true,
	}

	if !validTypes[opType] {
		return &ValidationError{
			Field:   "op_type",
			Value:   opType,
			Message: fmt.Sprintf("invalid operation type: %d", opType),
			Code:    "INVALID_OP_TYPE",
		}
	}

	return nil
}

// validateKeyValue validates key-value pair
func (vs *ValidationService) validateKeyValue(kv Types.KeyValue) error {
	// Validate key
	if err := vs.validateKey(kv.Key); err != nil {
		return err
	}

	// Validate value
	if err := vs.validateValue(kv.Value); err != nil {
		return err
	}

	return nil
}

// validateKey validates key constraints
func (vs *ValidationService) validateKey(key string) error {
	if key == "" {
		return &ValidationError{
			Field:   "key",
			Value:   key,
			Message: "key cannot be empty",
			Code:    "EMPTY_KEY",
		}
	}

	if len(key) > vs.maxKeyLength {
		return &ValidationError{
			Field:   "key",
			Value:   key,
			Message: fmt.Sprintf("key length %d exceeds maximum %d", len(key), vs.maxKeyLength),
			Code:    "KEY_TOO_LONG",
		}
	}

	// Check for invalid characters
	if strings.ContainsAny(key, "\x00\n\r\t") {
		return &ValidationError{
			Field:   "key",
			Value:   key,
			Message: "key contains invalid characters",
			Code:    "INVALID_KEY_CHARS",
		}
	}

	return nil
}

// validateValue validates value constraints
func (vs *ValidationService) validateValue(value string) error {
	if len(value) > vs.maxValueLength {
		return &ValidationError{
			Field:   "value",
			Value:   len(value),
			Message: fmt.Sprintf("value length %d exceeds maximum %d", len(value), vs.maxValueLength),
			Code:    "VALUE_TOO_LONG",
		}
	}

	return nil
}

// ValidateJSONMessage validates and parses a JSON message
func (vs *ValidationService) ValidateJSONMessage(jsonData string, target interface{}) error {
	// Validate message size first
	if err := vs.ValidateMessageSize(jsonData); err != nil {
		return err
	}

	// Parse JSON
	if err := json.Unmarshal([]byte(jsonData), target); err != nil {
		return &ValidationError{
			Field:   "json_data",
			Value:   jsonData[:min(100, len(jsonData))], // Truncate for logging
			Message: fmt.Sprintf("invalid JSON: %v", err),
			Code:    "INVALID_JSON",
		}
	}

	return nil
}

// LogValidationError logs validation errors with appropriate level
func (vs *ValidationService) LogValidationError(err error, context map[string]interface{}) {
	if ve, ok := err.(*ValidationError); ok {
		log.LogConsensusError(fmt.Sprintf("Validation failed: %s", ve.Error()), err,
			zap.String("field", ve.Field),
			zap.String("code", ve.Code),
			zap.Any("context", context),
			zap.String("function", "ValidationService.LogValidationError"))
	} else {
		log.LogConsensusError(fmt.Sprintf("Validation failed: %v", err), err,
			zap.Any("context", context),
			zap.String("function", "ValidationService.LogValidationError"))
	}
}

// GetValidationStats returns validation statistics
func (vs *ValidationService) GetValidationStats() map[string]interface{} {
	return map[string]interface{}{
		"max_message_size": vs.maxMessageSize,
		"max_key_length":   vs.maxKeyLength,
		"max_value_length": vs.maxValueLength,
		"allowed_stages":   len(vs.allowedStages),
		"validation_rules": []string{
			"message_size_limit",
			"key_length_limit",
			"value_length_limit",
			"peer_id_format",
			"timestamp_range",
			"ack_stage_validation",
			"operation_type_validation",
		},
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
