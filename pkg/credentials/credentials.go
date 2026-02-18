package credentials

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"

	"github.com/papercomputeco/tapes/pkg/dotdir"
)

const (
	credentialsFile = "credentials.toml"

	currentVersion = 0
)

// providerEnvVars maps provider names to their expected environment variables.
var providerEnvVars = map[string]string{
	"openai":    "OPENAI_API_KEY",
	"anthropic": "ANTHROPIC_API_KEY",
}

// Manager manages reading and writing credentials.toml in the .tapes/ directory.
type Manager struct {
	ddm        *dotdir.Manager
	targetPath string
}

// NewManager creates a new credentials Manager. If override is non-empty it is
// used as the .tapes/ directory; otherwise the standard dotdir resolution applies.
// When no .tapes/ directory is found, one is created at ~/.tapes/.
func NewManager(override string) (*Manager, error) {
	mgr := &Manager{}
	mgr.ddm = dotdir.NewManager()

	target, err := mgr.ddm.Target(override)
	if err != nil {
		return nil, err
	}

	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home dir: %w", err)
		}
		target = filepath.Join(home, ".tapes")
		if err := os.MkdirAll(target, 0o755); err != nil {
			return nil, fmt.Errorf("creating tapes dir: %w", err)
		}
	}

	mgr.targetPath = filepath.Join(target, credentialsFile)

	return mgr, nil
}

// Load reads credentials.toml from the target directory.
// Returns an empty Credentials if the file does not exist.
func (m *Manager) Load() (*Credentials, error) {
	data, err := os.ReadFile(m.targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Credentials{
				Version:   currentVersion,
				Providers: make(map[string]ProviderCredential),
			}, nil
		}
		return nil, fmt.Errorf("reading credentials: %w", err)
	}

	creds := &Credentials{}
	if err := toml.Unmarshal(data, creds); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}

	if creds.Providers == nil {
		creds.Providers = make(map[string]ProviderCredential)
	}

	return creds, nil
}

// Save writes credentials to credentials.toml with 0600 permissions.
func (m *Manager) Save(creds *Credentials) error {
	if creds == nil {
		return errors.New("cannot save nil credentials")
	}

	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	if err := encoder.Encode(creds); err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}

	if err := os.WriteFile(m.targetPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}

	return nil
}

// SetKey stores an API key for the given provider.
func (m *Manager) SetKey(provider, key string) error {
	creds, err := m.Load()
	if err != nil {
		return err
	}

	// API key and OAuth credentials are mutually exclusive per provider.
	creds.Providers[provider] = ProviderCredential{APIKey: key}

	return m.Save(creds)
}

// SetOAuth stores OAuth credentials for the given provider.
func (m *Manager) SetOAuth(provider string, oauth *OAuthCredential) error {
	if oauth == nil {
		return errors.New("oauth credentials cannot be nil")
	}
	if oauth.AccessToken == "" {
		return errors.New("oauth access token cannot be empty")
	}

	creds, err := m.Load()
	if err != nil {
		return err
	}

	// API key and OAuth credentials are mutually exclusive per provider.
	creds.Providers[provider] = ProviderCredential{
		OAuth: &OAuthCredential{
			AccessToken:  oauth.AccessToken,
			RefreshToken: oauth.RefreshToken,
			TokenType:    oauth.TokenType,
			Scope:        oauth.Scope,
			ExpiryUnix:   oauth.ExpiryUnix,
		},
	}

	return m.Save(creds)
}

// GetKey returns the stored API key for the given provider.
// Returns an empty string if no key is stored.
func (m *Manager) GetKey(provider string) (string, error) {
	creds, err := m.Load()
	if err != nil {
		return "", err
	}

	pc, ok := creds.Providers[provider]
	if !ok {
		return "", nil
	}

	return pc.APIKey, nil
}

// GetOAuth returns the stored OAuth credential for the given provider.
// Returns nil if no OAuth credential is stored.
func (m *Manager) GetOAuth(provider string) (*OAuthCredential, error) {
	creds, err := m.Load()
	if err != nil {
		return nil, err
	}

	pc, ok := creds.Providers[provider]
	if !ok || pc.OAuth == nil {
		return nil, nil
	}

	return &OAuthCredential{
		AccessToken:  pc.OAuth.AccessToken,
		RefreshToken: pc.OAuth.RefreshToken,
		TokenType:    pc.OAuth.TokenType,
		Scope:        pc.OAuth.Scope,
		ExpiryUnix:   pc.OAuth.ExpiryUnix,
	}, nil
}

// RemoveKey deletes the stored credential for a provider.
func (m *Manager) RemoveKey(provider string) error {
	creds, err := m.Load()
	if err != nil {
		return err
	}

	delete(creds.Providers, provider)

	return m.Save(creds)
}

// ListProviders returns the names of providers that have stored credentials.
func (m *Manager) ListProviders() ([]string, error) {
	creds, err := m.Load()
	if err != nil {
		return nil, err
	}

	providers := make([]string, 0, len(creds.Providers))
	for name, pc := range creds.Providers {
		hasAPIKey := pc.APIKey != ""
		hasOAuth := pc.OAuth != nil && (pc.OAuth.AccessToken != "" || pc.OAuth.RefreshToken != "")
		if !hasAPIKey && !hasOAuth {
			continue
		}
		providers = append(providers, name)
	}

	sort.Strings(providers)

	return providers, nil
}

// GetTarget returns the resolved path to the credentials file.
func (m *Manager) GetTarget() string {
	return m.targetPath
}

// EnvVarForProvider returns the environment variable name for a given provider.
// Returns an empty string for unknown providers.
func EnvVarForProvider(provider string) string {
	return providerEnvVars[provider]
}

// supportedProviders is the canonical list of providers managed by tapes auth.
var supportedProviders = []string{"openai", "anthropic"}

// SupportedProviders returns a copy of the supported provider list.
func SupportedProviders() []string {
	out := make([]string, len(supportedProviders))
	copy(out, supportedProviders)
	return out
}

// IsSupportedProvider returns true if the given provider is supported.
func IsSupportedProvider(provider string) bool {
	return slices.Contains(SupportedProviders(), provider)
}
