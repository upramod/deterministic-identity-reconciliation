// Package scim contains a small provider-neutral SCIM 2.0 target adapter.
// It intentionally supports only the read-before-write operations needed by
// the reconciliation contract.
package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/upramod/deterministic-identity-reconciliation/pkg/reconcile"
)

const coreUserSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
const patchOperationSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"

// DeprovisionMode controls what happens when the desired projection does not
// contain an identity.
type DeprovisionMode string

const (
	DeprovisionDisable DeprovisionMode = "disable"
	DeprovisionDelete  DeprovisionMode = "delete"
)

// Config configures a SCIM target.
type Config struct {
	BaseURL           string
	Token             string
	SubjectAttribute  string
	ManagedAttributes []string
	DeprovisionMode   DeprovisionMode
	HTTPClient        *http.Client
}

// Adapter implements reconcile.TargetAdapter for SCIM Users resources.
type Adapter struct {
	baseURL           *url.URL
	token             string
	subjectAttribute  string
	managedAttributes map[string]struct{}
	deprovisionMode   DeprovisionMode
	httpClient        *http.Client
}

// New validates configuration and creates a SCIM adapter.
func New(config Config) (*Adapter, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("BaseURL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("BaseURL must use HTTP or HTTPS")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("BaseURL cannot contain a query or fragment")
	}

	subjectAttribute := strings.TrimSpace(config.SubjectAttribute)
	if subjectAttribute == "" {
		subjectAttribute = "externalId"
	}
	if !validAttributeName(subjectAttribute) {
		return nil, fmt.Errorf("invalid SubjectAttribute %q", subjectAttribute)
	}
	if unsafeSubjectAttribute(subjectAttribute) {
		return nil, fmt.Errorf("SubjectAttribute %q is owned by the SCIM protocol or service provider", subjectAttribute)
	}

	deprovisionMode := config.DeprovisionMode
	if deprovisionMode == "" {
		deprovisionMode = DeprovisionDisable
	}
	if deprovisionMode != DeprovisionDisable && deprovisionMode != DeprovisionDelete {
		return nil, fmt.Errorf("unsupported DeprovisionMode %q", deprovisionMode)
	}

	managed := make(map[string]struct{}, len(config.ManagedAttributes))
	for _, attribute := range config.ManagedAttributes {
		attribute = strings.TrimSpace(attribute)
		if !validAttributeName(attribute) {
			return nil, fmt.Errorf("invalid managed attribute %q", attribute)
		}
		// SCIM attribute names are case-insensitive. The lookup binding must
		// remain immutable from the adapter's profile-attribute writes, or a
		// create or patch can make the resource unreachable by SubjectID.
		if strings.EqualFold(attribute, subjectAttribute) {
			return nil, fmt.Errorf("SubjectAttribute %q cannot also be a managed attribute", subjectAttribute)
		}
		// The adapter owns these protocol, identity, and lifecycle fields. A
		// profile value is always a string, so allowing (for example) active
		// here would replace the required Boolean during create and duplicate
		// the lifecycle operation during patch.
		if adapterOwnsAttribute(attribute) {
			return nil, fmt.Errorf("managed attribute %q is owned by the SCIM adapter", attribute)
		}
		managed[attribute] = struct{}{}
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	return &Adapter{
		baseURL:           parsed,
		token:             config.Token,
		subjectAttribute:  subjectAttribute,
		managedAttributes: managed,
		deprovisionMode:   deprovisionMode,
		httpClient:        httpClient,
	}, nil
}

// Observe reads one logical identity from the SCIM Users resource.
func (a *Adapter) Observe(ctx context.Context, subjectID string) (reconcile.ObservedState, error) {
	resource, err := a.find(ctx, subjectID)
	if err != nil {
		return reconcile.ObservedState{}, err
	}
	if resource == nil {
		return reconcile.ObservedState{
			Attributes: make(map[string]string),
		}, nil
	}

	active, err := scimActive(resource)
	if err != nil {
		return reconcile.ObservedState{}, err
	}
	attributes, err := a.managedValues(resource)
	if err != nil {
		return reconcile.ObservedState{}, err
	}
	return reconcile.ObservedState{
		Exists:     true,
		Enabled:    active,
		Attributes: attributes,
	}, nil
}

// StateEquivalent compares the observation using the configured deprovision
// policy. Disable mode intentionally retains the resource and its attributes;
// either an absent or a disabled account satisfies a desired absence.
// Physical existence remains visible in Observe and ConvergenceResult.
func (a *Adapter) StateEquivalent(observed reconcile.ObservedState, desired reconcile.DesiredState) bool {
	if !desired.Exists && a.deprovisionMode == DeprovisionDisable {
		return !observed.Exists || !observed.Enabled
	}
	return reconcile.StateEquivalent(observed, desired)
}

// Apply converges a SCIM Users resource after the caller has observed it.
//
// DeprovisionDisable is the default. It preserves the SCIM resource and sets
// active to false. DeprovisionDelete must be selected explicitly.
func (a *Adapter) Apply(ctx context.Context, desired reconcile.DesiredState) error {
	resource, err := a.find(ctx, desired.SubjectID)
	if err != nil {
		return err
	}

	if desired.Exists {
		if resource == nil {
			return a.create(ctx, desired)
		}
		id, err := resourceID(resource)
		if err != nil {
			return err
		}
		version, err := resourceVersion(resource)
		if err != nil {
			return err
		}
		return a.patch(ctx, id, desired.Enabled, desired.Attributes, version)
	}

	if resource == nil {
		return nil
	}
	id, err := resourceID(resource)
	if err != nil {
		return err
	}
	version, err := resourceVersion(resource)
	if err != nil {
		return err
	}
	if a.deprovisionMode == DeprovisionDelete {
		return a.request(ctx, http.MethodDelete, a.userURL(id), nil, nil, version)
	}
	return a.patch(ctx, id, false, nil, version)
}

func (a *Adapter) find(ctx context.Context, subjectID string) (map[string]any, error) {
	if strings.TrimSpace(subjectID) == "" {
		return nil, errors.New("subject ID is required")
	}
	filter := fmt.Sprintf("%s eq \"%s\"", a.subjectAttribute, escapeFilterValue(subjectID))
	usersURL := a.resourceURL("Users")
	query := usersURL.Query()
	query.Set("filter", filter)
	query.Set("count", "2")
	usersURL.RawQuery = query.Encode()

	var response struct {
		TotalResults *int             `json:"totalResults"`
		Resources    []map[string]any `json:"Resources"`
	}
	if err := a.request(ctx, http.MethodGet, usersURL.String(), nil, &response); err != nil {
		return nil, fmt.Errorf("find subject %q: %w", subjectID, err)
	}
	// count is only a maximum page size, not a guarantee of completeness.
	// RFC 7644 requires totalResults even when the server returns no matches.
	if response.TotalResults == nil || *response.TotalResults < 0 {
		return nil, errors.New("SCIM list response requires a non-negative totalResults")
	}
	if *response.TotalResults > 1 {
		return nil, fmt.Errorf("subject %q matched more than one SCIM resource", subjectID)
	}
	if *response.TotalResults != len(response.Resources) {
		return nil, errors.New("SCIM list response is incomplete or inconsistent with totalResults")
	}
	if len(response.Resources) == 0 {
		return nil, nil
	}
	id, err := resourceID(response.Resources[0])
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("SCIM resource did not include an id")
	}
	return response.Resources[0], nil
}

func (a *Adapter) create(ctx context.Context, desired reconcile.DesiredState) error {
	user := map[string]any{
		"schemas":    []string{coreUserSchema},
		"userName":   desired.SubjectID,
		"externalId": desired.SubjectID,
		"active":     desired.Enabled,
	}
	// The configured lookup key is identity binding, not an optional profile
	// attribute. Always persist it on creation so the next lookup can find the
	// resource even when callers exclude it from managed fields.
	user[a.subjectAttribute] = desired.SubjectID
	for key, value := range desired.Attributes {
		if _, managed := a.managedAttributes[key]; managed {
			user[key] = value
		}
	}
	return a.request(ctx, http.MethodPost, a.resourceURL("Users").String(), user, nil)
}

func (a *Adapter) patch(ctx context.Context, id string, enabled bool, attributes map[string]string, version string) error {
	if id == "" {
		return errors.New("SCIM resource id is required")
	}
	operations := []map[string]any{
		{
			"op":    "replace",
			"path":  "active",
			"value": enabled,
		},
	}
	for key, value := range attributes {
		if _, managed := a.managedAttributes[key]; !managed {
			continue
		}
		operations = append(operations, map[string]any{
			"op":    "replace",
			"path":  key,
			"value": value,
		})
	}
	body := map[string]any{
		"schemas":    []string{patchOperationSchema},
		"Operations": operations,
	}
	return a.request(ctx, http.MethodPatch, a.userURL(id), body, nil, version)
}

func (a *Adapter) request(ctx context.Context, method, endpoint string, body any, response any, versions ...string) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode SCIM request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return fmt.Errorf("build SCIM request: %w", err)
	}
	req.Header.Set("Accept", "application/scim+json")
	if len(versions) > 0 && versions[0] != "" {
		req.Header.Set("If-Match", versions[0])
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send SCIM request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return fmt.Errorf("SCIM request returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if response == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4*1024*1024))
	decoder.UseNumber()
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("decode SCIM response: %w", err)
	}
	return nil
}

func (a *Adapter) resourceURL(resource string) *url.URL {
	result := *a.baseURL
	result.Path = strings.TrimRight(result.Path, "/") + "/" + strings.TrimLeft(resource, "/")
	return &result
}

func (a *Adapter) userURL(id string) string {
	result := a.resourceURL("Users/" + url.PathEscape(id))
	return result.String()
}

func (a *Adapter) managedValues(resource map[string]any) (map[string]string, error) {
	values := make(map[string]string)
	for attribute := range a.managedAttributes {
		candidate, found, err := responseAttribute(resource, attribute)
		if err != nil {
			return nil, err
		}
		if value, ok := scalarString(candidate); found && ok {
			values[attribute] = value
		}
	}
	return values, nil
}

func resourceID(resource map[string]any) (string, error) {
	value, _, err := responseAttribute(resource, "id")
	if err != nil {
		return "", err
	}
	id, _ := value.(string)
	return strings.TrimSpace(id), nil
}

func resourceVersion(resource map[string]any) (string, error) {
	value, _, err := responseAttribute(resource, "meta")
	if err != nil {
		return "", err
	}
	meta, _ := value.(map[string]any)
	value, _, err = responseAttribute(meta, "version")
	if err != nil {
		return "", err
	}
	version, _ := value.(string)
	return strings.TrimSpace(version), nil
}

func scimActive(resource map[string]any) (bool, error) {
	value, _, err := responseAttribute(resource, "active")
	if err != nil {
		return false, err
	}
	active, ok := value.(bool)
	if !ok {
		return true, nil
	}
	return active, nil
}

func responseAttribute(resource map[string]any, attribute string) (any, bool, error) {
	var value any
	found := false
	for key, candidate := range resource {
		if !strings.EqualFold(key, attribute) {
			continue
		}
		if found {
			return nil, false, fmt.Errorf("SCIM response contains ambiguous case variants of attribute %q", attribute)
		}
		value = candidate
		found = true
	}
	return value, found, nil
}

func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func escapeFilterValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func validAttributeName(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if index == 0 && !isLetter(character) {
			return false
		}
		if !isLetter(character) && (character < '0' || character > '9') &&
			character != '_' && character != '-' && character != ':' && character != '.' {
			return false
		}
	}
	return true
}

func adapterOwnsAttribute(value string) bool {
	switch strings.ToLower(value) {
	case "schemas", "username", "externalid", "active", "id", "meta":
		return true
	default:
		return false
	}
}

func unsafeSubjectAttribute(value string) bool {
	switch strings.ToLower(value) {
	case "schemas", "active", "id", "meta":
		return true
	default:
		return false
	}
}

func isLetter(value rune) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
