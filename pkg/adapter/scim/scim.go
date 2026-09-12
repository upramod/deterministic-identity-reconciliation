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

	active := scimActive(resource)
	return reconcile.ObservedState{
		Exists:     true,
		Enabled:    active,
		Attributes: a.managedValues(resource),
	}, nil
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
		return a.patch(ctx, resourceID(resource), desired.Enabled, desired.Attributes)
	}

	if resource == nil {
		return nil
	}
	if a.deprovisionMode == DeprovisionDelete {
		return a.request(ctx, http.MethodDelete, a.userURL(resourceID(resource)), nil, nil)
	}
	return a.patch(ctx, resourceID(resource), false, nil)
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
		Resources []map[string]any `json:"Resources"`
	}
	if err := a.request(ctx, http.MethodGet, usersURL.String(), nil, &response); err != nil {
		return nil, fmt.Errorf("find subject %q: %w", subjectID, err)
	}
	if len(response.Resources) == 0 {
		return nil, nil
	}
	if len(response.Resources) > 1 {
		return nil, fmt.Errorf("subject %q matched more than one SCIM resource", subjectID)
	}
	if resourceID(response.Resources[0]) == "" {
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
	for key, value := range desired.Attributes {
		if _, managed := a.managedAttributes[key]; managed {
			user[key] = value
		}
	}
	if _, managed := a.managedAttributes[a.subjectAttribute]; managed {
		user[a.subjectAttribute] = desired.SubjectID
	}
	return a.request(ctx, http.MethodPost, a.resourceURL("Users").String(), user, nil)
}

func (a *Adapter) patch(ctx context.Context, id string, enabled bool, attributes map[string]string) error {
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
	return a.request(ctx, http.MethodPatch, a.userURL(id), body, nil)
}

func (a *Adapter) request(ctx context.Context, method, endpoint string, body any, response any) error {
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

func (a *Adapter) managedValues(resource map[string]any) map[string]string {
	values := make(map[string]string)
	for attribute := range a.managedAttributes {
		if value, ok := scalarString(resource[attribute]); ok {
			values[attribute] = value
		}
	}
	return values
}

func resourceID(resource map[string]any) string {
	value, _ := resource["id"].(string)
	return strings.TrimSpace(value)
}

func scimActive(resource map[string]any) bool {
	active, ok := resource["active"].(bool)
	if !ok {
		return true
	}
	return active
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

func isLetter(value rune) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}
