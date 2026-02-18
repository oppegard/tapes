package credentials

// Credentials represents the stored API credentials in credentials.toml.
type Credentials struct {
	Version   int                           `toml:"version"`
	Providers map[string]ProviderCredential `toml:"providers"`
}

// ProviderCredential holds credentials for a single provider.
type ProviderCredential struct {
	APIKey string           `toml:"api_key,omitempty"`
	OAuth  *OAuthCredential `toml:"oauth,omitempty"`
}

// OAuthCredential holds OAuth credentials for a provider.
type OAuthCredential struct {
	AccessToken  string `toml:"access_token"`
	RefreshToken string `toml:"refresh_token,omitempty"`
	TokenType    string `toml:"token_type,omitempty"`
	Scope        string `toml:"scope,omitempty"`
	ExpiryUnix   int64  `toml:"expiry_unix,omitempty"`
}
