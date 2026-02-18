package authcmder

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/papercomputeco/tapes/pkg/credentials"
)

const (
	defaultOpenAIOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	//nolint:gosec // OAuth endpoint URL, not a credential.
	defaultOpenAIOAuthTokenURL     = "https://auth.openai.com/oauth/token"
	defaultOpenAIOAuthClientID     = "codex-cli"
	defaultOpenAIOAuthScope        = "openid profile email offline_access"
	defaultOpenAIOAuthAudience     = "https://api.openai.com/v1"
	defaultOpenAIOAuthCallbackPath = "/oauth/callback"
	defaultOpenAIOAuthTimeout      = 2 * time.Minute
)

type openAIOAuthConfig struct {
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	Scope        string
	Audience     string
	CallbackPath string
	Timeout      time.Duration
}

type oauthCallbackResult struct {
	Code  string
	State string
	Err   string
}

type openAITokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	ExpiresInSeconds int64  `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func loadOpenAIOAuthConfig() openAIOAuthConfig {
	cfg := openAIOAuthConfig{
		AuthorizeURL: defaultOpenAIOAuthAuthorizeURL,
		TokenURL:     defaultOpenAIOAuthTokenURL,
		ClientID:     defaultOpenAIOAuthClientID,
		Scope:        defaultOpenAIOAuthScope,
		Audience:     defaultOpenAIOAuthAudience,
		CallbackPath: defaultOpenAIOAuthCallbackPath,
		Timeout:      defaultOpenAIOAuthTimeout,
	}

	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_AUTHORIZE_URL")); v != "" {
		cfg.AuthorizeURL = v
	}
	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_TOKEN_URL")); v != "" {
		cfg.TokenURL = v
	}
	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_CLIENT_ID")); v != "" {
		cfg.ClientID = v
	}
	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_SCOPE")); v != "" {
		cfg.Scope = v
	}
	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_AUDIENCE")); v != "" {
		cfg.Audience = v
	}
	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_CALLBACK_PATH")); v != "" {
		cfg.CallbackPath = v
	}
	if v := strings.TrimSpace(os.Getenv("TAPES_OPENAI_OAUTH_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil && d > 0 {
			cfg.Timeout = d
		}
	}

	if !strings.HasPrefix(cfg.CallbackPath, "/") {
		cfg.CallbackPath = "/" + cfg.CallbackPath
	}

	return cfg
}

func runOpenAIOAuthFlow(
	ctx context.Context,
	out io.Writer,
	httpClient *http.Client,
	cfg openAIOAuthConfig,
) (*credentials.OAuthCredential, error) {
	if out == nil {
		out = os.Stdout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = defaultOpenAIOAuthAuthorizeURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = defaultOpenAIOAuthTokenURL
	}
	if cfg.ClientID == "" {
		cfg.ClientID = defaultOpenAIOAuthClientID
	}
	if cfg.Scope == "" {
		cfg.Scope = defaultOpenAIOAuthScope
	}
	if cfg.CallbackPath == "" {
		cfg.CallbackPath = defaultOpenAIOAuthCallbackPath
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultOpenAIOAuthTimeout
	}
	if !strings.HasPrefix(cfg.CallbackPath, "/") {
		cfg.CallbackPath = "/" + cfg.CallbackPath
	}

	state, err := randomURLSafeString(32)
	if err != nil {
		return nil, fmt.Errorf("generating oauth state: %w", err)
	}
	codeVerifier, err := randomURLSafeString(32)
	if err != nil {
		return nil, fmt.Errorf("generating pkce verifier: %w", err)
	}
	codeChallenge := pkceChallengeS256(codeVerifier)

	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting oauth callback listener: %w", err)
	}
	defer func() { _ = listener.Close() }()

	redirectURI := fmt.Sprintf("http://%s%s", listener.Addr().String(), cfg.CallbackPath)
	callbackCh := make(chan oauthCallbackResult, 1)
	serveErrCh := make(chan error, 1)
	var callbackOnce sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		result := oauthCallbackResult{
			Code:  strings.TrimSpace(q.Get("code")),
			State: strings.TrimSpace(q.Get("state")),
		}

		if errCode := strings.TrimSpace(q.Get("error")); errCode != "" {
			desc := strings.TrimSpace(q.Get("error_description"))
			if desc != "" {
				result.Err = fmt.Sprintf("oauth callback error: %s (%s)", errCode, desc)
			} else {
				result.Err = "oauth callback error: " + errCode
			}
		}

		callbackOnce.Do(func() {
			callbackCh <- result
		})

		status := http.StatusOK
		body := "Authentication received. You can close this tab and return to tapes."
		if result.Err != "" {
			status = http.StatusBadRequest
			body = "Authentication failed. Return to tapes for details."
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serveErrCh <- serveErr
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	authURL, err := buildOpenAIAuthorizeURL(cfg, redirectURI, state, codeChallenge)
	if err != nil {
		return nil, err
	}

	fmt.Fprintln(out, "Open this URL in your browser to authenticate OpenAI:")
	fmt.Fprintln(out, authURL)
	fmt.Fprintln(out)

	timeout := time.NewTimer(cfg.Timeout)
	defer timeout.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case serveErr := <-serveErrCh:
		return nil, fmt.Errorf("oauth callback server failed: %w", serveErr)
	case <-timeout.C:
		return nil, errors.New("timed out waiting for oauth callback")
	case cb := <-callbackCh:
		if cb.Err != "" {
			return nil, errors.New(cb.Err)
		}
		if cb.Code == "" {
			return nil, errors.New("oauth callback did not include an authorization code")
		}
		if cb.State != state {
			return nil, errors.New("oauth state mismatch")
		}

		token, err := exchangeOpenAICodeForToken(ctx, httpClient, cfg, cb.Code, codeVerifier, redirectURI)
		if err != nil {
			return nil, err
		}

		expiryUnix := int64(0)
		if token.ExpiresInSeconds > 0 {
			expiryUnix = time.Now().Add(time.Duration(token.ExpiresInSeconds) * time.Second).Unix()
		}

		scope := token.Scope
		if scope == "" {
			scope = cfg.Scope
		}

		return &credentials.OAuthCredential{
			AccessToken:  token.AccessToken,
			RefreshToken: token.RefreshToken,
			TokenType:    token.TokenType,
			Scope:        scope,
			ExpiryUnix:   expiryUnix,
		}, nil
	}
}

func buildOpenAIAuthorizeURL(cfg openAIOAuthConfig, redirectURI, state, codeChallenge string) (string, error) {
	authURL, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("invalid openai authorize url: %w", err)
	}

	q := authURL.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", cfg.Scope)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if cfg.Audience != "" {
		q.Set("audience", cfg.Audience)
	}
	authURL.RawQuery = q.Encode()

	return authURL.String(), nil
}

func exchangeOpenAICodeForToken(
	ctx context.Context,
	httpClient *http.Client,
	cfg openAIOAuthConfig,
	code, codeVerifier, redirectURI string,
) (*openAITokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", cfg.ClientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting oauth token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	var parsed openAITokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(parsed.ErrorDescription)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Error)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("oauth token exchange failed (%d): %s", resp.StatusCode, msg)
	}

	if parsed.AccessToken == "" {
		return nil, errors.New("oauth token response missing access_token")
	}

	return &parsed, nil
}

func randomURLSafeString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
