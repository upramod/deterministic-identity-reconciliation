package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type hashEnvelope struct {
	SubjectID        string         `json:"subject_id"`
	EventType        EventType      `json:"event_type"`
	SourceScope      SourceScope    `json:"source_scope"`
	EffectiveTime    string         `json:"effective_time"`
	EndTime          *string        `json:"end_time,omitempty"`
	ModificationTime string         `json:"modification_time"`
	SourceSequence   int64          `json:"source_sequence"`
	RevisionNumber   int64          `json:"revision_number"`
	Payload          map[string]any `json:"payload"`
}

// Canonicalize normalizes an immutable event and attaches the ingestion clock.
// Delivery time is retained for audit but is excluded from payload hashing.
func Canonicalize(event PhysicalEvent, receivedAt time.Time) (CanonicalFact, error) {
	subjectID := strings.TrimSpace(event.SubjectID)
	if subjectID == "" {
		return CanonicalFact{}, errors.New("subject_id is required")
	}
	eventKey := strings.TrimSpace(event.EventKey)
	if eventKey == "" {
		return CanonicalFact{}, errors.New("event_key is required")
	}
	eventType := EventType(strings.ToLower(strings.TrimSpace(string(event.EventType))))
	if !validEventType(eventType) {
		return CanonicalFact{}, fmt.Errorf("unsupported event_type %q", event.EventType)
	}
	sourceScope := SourceScope(strings.ToLower(strings.TrimSpace(string(event.SourceScope))))
	if sourceScope != SourceHireSnapshot && sourceScope != SourceLifecycle {
		return CanonicalFact{}, fmt.Errorf("unsupported source_scope %q", event.SourceScope)
	}
	if event.EffectiveTime.IsZero() {
		return CanonicalFact{}, errors.New("effective_time is required")
	}
	if event.ModificationTime.IsZero() {
		return CanonicalFact{}, errors.New("modification_time is required")
	}
	if event.SourceSequence < 0 {
		return CanonicalFact{}, errors.New("source_sequence cannot be negative")
	}
	if event.RevisionNumber < 0 {
		return CanonicalFact{}, errors.New("revision_number cannot be negative")
	}

	effectiveTime := normalizeTime(event.EffectiveTime)
	modificationTime := normalizeTime(event.ModificationTime)
	var endTime *time.Time
	if event.EndTime != nil {
		normalizedEndTime := normalizeTime(*event.EndTime)
		endTime = &normalizedEndTime
	}
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	attributes, err := attributesFromPayload(payload)
	if err != nil {
		return CanonicalFact{}, err
	}

	envelope := hashEnvelope{
		SubjectID:        subjectID,
		EventType:        eventType,
		SourceScope:      sourceScope,
		EffectiveTime:    effectiveTime.Format(time.RFC3339Nano),
		ModificationTime: modificationTime.Format(time.RFC3339Nano),
		SourceSequence:   event.SourceSequence,
		RevisionNumber:   event.RevisionNumber,
		Payload:          payload,
	}
	if endTime != nil {
		formattedEndTime := endTime.Format(time.RFC3339Nano)
		envelope.EndTime = &formattedEndTime
	}
	encodedEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return CanonicalFact{}, fmt.Errorf("marshal canonical envelope: %w", err)
	}
	hash := sha256.Sum256(encodedEnvelope)

	received := normalizeTime(receivedAt)
	if received.IsZero() {
		received = modificationTime
	}

	return CanonicalFact{
		FactKey:          makeFactKey(subjectID, sourceScope, eventType, effectiveTime),
		EventKey:         eventKey,
		SubjectID:        subjectID,
		EventType:        eventType,
		SourceScope:      sourceScope,
		EffectiveTime:    effectiveTime,
		EndTime:          endTime,
		ModificationTime: modificationTime,
		ReceivedAt:       received,
		Freshness: FreshnessTuple{
			SourceSequence:   event.SourceSequence,
			ModificationTime: modificationTime,
			RevisionNumber:   event.RevisionNumber,
			PhysicalEventKey: eventKey,
		},
		PayloadHash: hex.EncodeToString(hash[:]),
		Payload:     payload,
		Attributes:  attributes,
	}, nil
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventHire, EventRehire, EventDataUpdate, EventLeave,
		EventReturnFromLeave, EventConversion, EventTransfer, EventTermination:
		return true
	default:
		return false
	}
}

func makeFactKey(subjectID string, sourceScope SourceScope, eventType EventType, effectiveTime time.Time) string {
	return strings.Join([]string{
		subjectID,
		string(sourceScope),
		string(eventType),
		effectiveTime.Format(time.RFC3339Nano),
	}, "\x1f")
}

func normalizeTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.Round(0).UTC()
}

func attributesFromPayload(payload map[string]any) (map[string]string, error) {
	attributes := make(map[string]string)
	raw, ok := payload["attributes"]
	if !ok {
		return attributes, nil
	}

	values := make(map[string]any)
	switch typed := raw.(type) {
	case map[string]any:
		values = typed
	case map[string]string:
		for key, value := range typed {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seenNames := make(map[string]string, len(keys))
	for _, key := range keys {
		name := strings.TrimSpace(key)
		if name == "" {
			return nil, fmt.Errorf("attribute name %q is empty after trimming", key)
		}
		if previous, exists := seenNames[name]; exists {
			return nil, fmt.Errorf("attribute names %q and %q normalize to %q", previous, key, name)
		}
		seenNames[name] = key
		if normalized, ok := scalarString(values[key]); ok {
			attributes[name] = normalized
		}
	}
	return attributes, nil
}

func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}
