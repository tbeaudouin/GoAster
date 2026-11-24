// Package migration implements the source-side one-shot central migration
// create contract. Provider credentials and private handoff material stay out
// of model-visible results and ordinary cron messages.
package migration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"goaster/internal/localenv"
)

const MigrationWebOrigin = "https://goasterweb-thomas.fly.dev"

var (
	errInvalidRequest = errors.New("invalid migration request")

	// ErrCredentialStorageUnreadable identifies settings that cannot be read
	// safely enough to obtain a provider credential.
	ErrCredentialStorageUnreadable = errors.New("migration credential storage is unreadable")
	// ErrHetznerTokenInvalid identifies a malformed configured provider token.
	ErrHetznerTokenInvalid = errors.New("HCLOUD_TOKEN is invalid")
	// ErrHetznerTokenMissing identifies an absent HCLOUD_TOKEN setting.
	ErrHetznerTokenMissing = errors.New("HCLOUD_TOKEN is missing")
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Request is the exact non-secret input accepted by the active create tool.
// The server name is required and must be the approved fixed migration name;
// an empty location list permits any compatible location.
type Request struct {
	ServerType string   `json:"server_type"`
	ServerName string   `json:"server_name"`
	Locations  []string `json:"locations,omitempty"`
}

// Options contains the active one-shot seams. The split handoff callback sends
// the setup URL and private key as separate owner-only delivery parts.
type Options struct {
	WebOrigin                    string
	HTTPClient                   httpDoer
	LoadHetznerToken             func() (string, error)
	MigrationRecipient           func() (int64, error)
	PrepareRecipientHandoffParts func(int64) (func(context.Context, string, []byte) error, error)
	ReportOutcome                func(context.Context, int64, OneShotResult) error
	AllowInsecureHTTP            bool
}

// DefaultOptions returns the fixed HTTPS origin and a client that neither uses
// a proxy nor follows redirects. It performs no network or filesystem I/O.
func DefaultOptions() Options {
	return Options{
		WebOrigin:        MigrationWebOrigin,
		HTTPClient:       &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: noRedirect},
		LoadHetznerToken: loadHetznerToken,
	}
}

// ValidMigrationServerName accepts only the fixed active one-shot migration
// name format.
func ValidMigrationServerName(value string) bool {
	const prefix = "goaster-migration-"
	if len(value) != len(prefix)+16 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validateMachineRequest(serverType string, locations []string) error {
	if !safeMachineName(serverType, 128) {
		return errInvalidRequest
	}
	if len(locations) > 64 {
		return errInvalidRequest
	}
	seen := make(map[string]struct{}, len(locations))
	for _, location := range locations {
		if !safeMachineName(location, 128) {
			return errInvalidRequest
		}
		if _, exists := seen[location]; exists {
			return errInvalidRequest
		}
		seen[location] = struct{}{}
	}
	return nil
}

// ValidateRequest checks the exact non-secret parameters for a create attempt.
func ValidateRequest(request Request) error {
	return validateOneShotRequest(request)
}

func validateOneShotRequest(request Request) error {
	if err := validateMachineRequest(request.ServerType, request.Locations); err != nil {
		return err
	}
	if !ValidMigrationServerName(request.ServerName) {
		return errInvalidRequest
	}
	return nil
}

func generateLoginMaterial() (string, []byte, string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, "", err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	clear(private)
	if err != nil {
		return "", nil, "", err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	clear(privateDER)
	if len(privatePEM) == 0 {
		return "", nil, "", errors.New("private key encoding failed")
	}
	publicWire := sshWireString([]byte("ssh-ed25519"))
	publicWire = append(publicWire, sshWireString(public)...)
	publicValue := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(publicWire)
	var secret [32]byte
	if _, err := io.ReadFull(rand.Reader, secret[:]); err != nil {
		clear(privatePEM)
		return "", nil, "", err
	}
	ownerSecret := base64.RawURLEncoding.EncodeToString(secret[:])
	clear(secret[:])
	return publicValue, privatePEM, ownerSecret, nil
}

func sshWireString(value []byte) []byte {
	wire := make([]byte, 4+len(value))
	binary.BigEndian.PutUint32(wire[:4], uint32(len(value)))
	copy(wire[4:], value)
	return wire
}

func webEndpoint(origin, path string, allowInsecure bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSuffix(origin, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("migration Web origin is invalid")
	}
	if parsed.Scheme != "https" && !(allowInsecure && parsed.Scheme == "http") {
		return "", errors.New("migration Web origin must use HTTPS")
	}
	parsed.Path = path
	parsed.RawPath = ""
	return parsed.String(), nil
}

// decodeUniqueTopLevelFields inspects only the top-level object shape. It is
// deliberately separate from typed decoding so safe creation evidence can be
// recovered when an error envelope is malformed without accepting that
// envelope. Unknown fields are shape-checked and then discarded; they are
// never copied into a result.
func decodeUniqueTopLevelFields(data []byte) (map[string]json.RawMessage, error) {
	inspector := json.NewDecoder(bytes.NewReader(data))
	token, err := inspector.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("migration JSON must be an object")
	}
	fields := make(map[string]json.RawMessage)
	seen := make(map[string]struct{})
	for inspector.More() {
		token, err := inspector.Token()
		field, ok := token.(string)
		if err != nil || !ok || field != strings.ToLower(field) {
			return nil, errors.New("migration JSON field names must be unique and lower-case")
		}
		if _, exists := seen[field]; exists {
			return nil, errors.New("migration JSON field names must be unique and lower-case")
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := inspector.Decode(&value); err != nil {
			return nil, fmt.Errorf("inspect migration JSON value: %w", err)
		}
		switch field {
		case "creation_state", "server_id", "server_label":
			fields[field] = value
		}
	}
	if token, err := inspector.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("migration JSON object is incomplete")
	}
	var extra json.RawMessage
	if err := inspector.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("migration JSON contains trailing data")
		}
		return nil, fmt.Errorf("decode migration trailing data: %w", err)
	}
	return fields, nil
}

func decodeExactJSON(data []byte, output any) error {
	if _, err := decodeUniqueTopLevelFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode migration JSON: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("migration JSON contains trailing data")
		}
		return fmt.Errorf("decode migration trailing data: %w", err)
	}
	return nil
}

func completeOptions(options Options) Options {
	defaults := DefaultOptions()
	if options.WebOrigin == "" {
		options.WebOrigin = defaults.WebOrigin
	}
	if options.HTTPClient == nil {
		options.HTTPClient = defaults.HTTPClient
	}
	if options.LoadHetznerToken == nil {
		options.LoadHetznerToken = defaults.LoadHetznerToken
	}
	return options
}

func loadHetznerToken() (string, error) {
	store, err := localenv.NewStore()
	savedTokenFound := false
	if err == nil {
		err = store.Load()
	}
	if err != nil {
		return "", errors.Join(ErrCredentialStorageUnreadable, fmt.Errorf("load Hetzner settings: %w", err))
	}
	if token, ok := store.Values()["HCLOUD_TOKEN"]; ok {
		savedTokenFound = true
		if validHetznerToken(token) {
			return token, nil
		}
	}
	environmentTokenInvalid := false
	if token, ok := os.LookupEnv("HCLOUD_TOKEN"); ok && token != "" {
		if validHetznerToken(token) {
			return token, nil
		}
		environmentTokenInvalid = true
	}
	if savedTokenFound || environmentTokenInvalid {
		return "", ErrHetznerTokenInvalid
	}
	return "", ErrHetznerTokenMissing
}

func validHetznerToken(value string) bool {
	return len(value) > 0 && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t ")
}

func safeName(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n\t")
}

func safeMachineName(value string, limit int) bool {
	if !safeName(value, limit) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
