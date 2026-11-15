package migration

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"goaster/internal/protectedfile"
)

type machineTerms struct {
	OperationID         string   `json:"operation_id"`
	OperationCapability string   `json:"operation_capability"`
	ServerType          string   `json:"server_type"`
	Locations           []string `json:"locations"`
	Image               string   `json:"image"`
	Architecture        string   `json:"architecture"`
	EnableIPv4          bool     `json:"enable_ipv4"`
	EnableIPv6          bool     `json:"enable_ipv6"`
	Networks            []string `json:"networks"`
	BillingTerms        string   `json:"billing_terms"`
	LoginPublicKey      string   `json:"login_public_key"`
	OwnerSecretDigest   string   `json:"owner_secret_digest"`
}
type operationState struct {
	Terms       machineTerms `json:"terms"`
	PrivateKey  []byte       `json:"private_key"`
	OwnerSecret string       `json:"owner_secret"`
	Recipient   int64        `json:"recipient"`
}

func operationDirectory(options Options) (string, error) {
	if options.TempDirectory != "" {
		return migrationTaskDirectory(options.TempDirectory)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".goaster", "migration-monitor", "operations")
	if err = ensurePrivatePath(dir); err != nil {
		return "", err
	}
	return dir, nil
}
func newOperation(request Request, recipient int64) (operationState, error) {
	var id, capability [32]byte
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		return operationState{}, err
	}
	if _, err := io.ReadFull(rand.Reader, capability[:]); err != nil {
		return operationState{}, err
	}
	pub, priv, secret, digest, err := generateLoginMaterial()
	if err != nil {
		return operationState{}, err
	}
	locations := append([]string{}, request.Locations...)
	return operationState{Terms: machineTerms{OperationID: hex.EncodeToString(id[:]), OperationCapability: base64.RawURLEncoding.EncodeToString(capability[:]), ServerType: request.ServerType, Locations: locations, Image: "ubuntu-24.04", Architecture: "x86", EnableIPv4: true, Networks: []string{}, BillingTerms: "provider-charges-uncapped-v1", LoginPublicKey: pub, OwnerSecretDigest: digest}, PrivateKey: priv, OwnerSecret: secret, Recipient: recipient}, nil
}
func validateOperation(s operationState, request Request, recipient int64) error {
	t := s.Terms
	id, e := hex.DecodeString(t.OperationID)
	if e != nil || len(id) != 32 || hex.EncodeToString(id) != t.OperationID || !validOwnerSecret(t.OperationCapability) || !validOwnerSecret(s.OwnerSecret) || s.Recipient != recipient || t.ServerType != request.ServerType || !reflect.DeepEqual(t.Locations, append([]string{}, request.Locations...)) || t.Image != "ubuntu-24.04" || t.Architecture != "x86" || !t.EnableIPv4 || t.EnableIPv6 || t.Networks == nil || len(t.Networks) != 0 || t.BillingTerms != "provider-charges-uncapped-v1" {
		return errInvalidRequest
	}
	secret, _ := base64.RawURLEncoding.DecodeString(s.OwnerSecret)
	digest := sha256.Sum256(secret)
	if hex.EncodeToString(digest[:]) != t.OwnerSecretDigest {
		return errInvalidRequest
	}
	block, rest := pem.Decode(s.PrivateKey)
	if block == nil || len(rest) != 0 || block.Type != "PRIVATE KEY" {
		return errInvalidRequest
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return errInvalidRequest
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok || len(private) != ed25519.PrivateKeySize {
		return errInvalidRequest
	}
	wire := sshWireString([]byte("ssh-ed25519"))
	wire = append(wire, sshWireString(private.Public().(ed25519.PublicKey))...)
	if t.LoginPublicKey != "ssh-ed25519 "+base64.StdEncoding.EncodeToString(wire) {
		return errInvalidRequest
	}
	return nil
}
func storeOperation(dir, job string, s operationState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	defer clear(data)
	digest := sha256.Sum256(data)
	if _, err = protectedfile.WriteExclusive(filepath.Join(dir, job+".digest"), []byte(hex.EncodeToString(digest[:]))); err != nil {
		return err
	}
	_, err = protectedfile.WriteExclusive(filepath.Join(dir, job+".json"), data)
	return err
}
func loadOperation(dir, job string, request Request, recipient int64) (operationState, error) {
	content, err := protectedfile.Read(filepath.Join(dir, job+".json"), webRequestMaxBytes)
	if err != nil {
		return operationState{}, err
	}
	defer clear(content.Data)
	manifest, err := protectedfile.Read(filepath.Join(dir, job+".digest"), 64)
	if err != nil {
		return operationState{}, err
	}
	defer clear(manifest.Data)
	digest := sha256.Sum256(content.Data)
	if string(manifest.Data) != hex.EncodeToString(digest[:]) {
		return operationState{}, errInvalidRequest
	}
	var s operationState
	if err = decodeExactJSON(content.Data, &s); err != nil {
		return operationState{}, err
	}
	if err = validateOperation(s, request, recipient); err != nil {
		return operationState{}, err
	}
	return s, nil
}
func markDispatch(dir, job string) error {
	_, err := protectedfile.WriteExclusive(filepath.Join(dir, job+".create_dispatching"), []byte("create_dispatching\n"))
	if err != nil {
		return errors.Join(errManualReview, err)
	}
	return nil
}
func machinePayload(t machineTerms, token string) ([]byte, error) {
	return json.Marshal(struct {
		machineTerms
		HetznerToken string `json:"hetzner_token"`
	}{t, token})
}
