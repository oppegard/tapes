package authcmder

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("runOpenAIOAuthFlow", func() {
	It("rejects callback with mismatched state", func() {
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer"}`))
		}))
		defer tokenServer.Close()

		cfg := openAIOAuthConfig{
			AuthorizeURL: "https://auth.example.test/oauth/authorize",
			TokenURL:     tokenServer.URL,
			ClientID:     "test-client-id",
			Scope:        "openid profile",
			Audience:     "https://api.openai.com/v1",
			CallbackPath: "/oauth/callback",
			Timeout:      3 * time.Second,
		}

		var out bytes.Buffer
		errCh := make(chan error, 1)

		go func() {
			_, err := runOpenAIOAuthFlow(context.Background(), &out, tokenServer.Client(), cfg)
			errCh <- err
		}()

		var authURL string
		Eventually(func() string {
			authURL = firstURLInOutput(out.String())
			return authURL
		}, 2*time.Second, 20*time.Millisecond).ShouldNot(BeEmpty())

		parsed, err := url.Parse(authURL)
		Expect(err).NotTo(HaveOccurred())
		redirectURI := parsed.Query().Get("redirect_uri")
		Expect(redirectURI).NotTo(BeEmpty())

		resp, err := http.Get(redirectURI + "?code=test-code&state=wrong-state")
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())

		Eventually(errCh, 2*time.Second, 20*time.Millisecond).Should(Receive(MatchError(ContainSubstring("oauth state mismatch"))))
	})

	It("times out while waiting for callback", func() {
		cfg := openAIOAuthConfig{
			AuthorizeURL: "https://auth.example.test/oauth/authorize",
			TokenURL:     "https://auth.example.test/oauth/token",
			ClientID:     "test-client-id",
			Scope:        "openid profile",
			Audience:     "https://api.openai.com/v1",
			CallbackPath: "/oauth/callback",
			Timeout:      100 * time.Millisecond,
		}

		_, err := runOpenAIOAuthFlow(context.Background(), &bytes.Buffer{}, &http.Client{Timeout: time.Second}, cfg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("timed out waiting for oauth callback"))
	})
})

var _ = Describe("exchangeOpenAICodeForToken", func() {
	It("sends required form fields during token exchange", func() {
		received := map[string]string{}
		var receivedContentType string

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.ParseForm()).To(Succeed())

			received["grant_type"] = r.PostForm.Get("grant_type")
			received["client_id"] = r.PostForm.Get("client_id")
			received["code"] = r.PostForm.Get("code")
			received["redirect_uri"] = r.PostForm.Get("redirect_uri")
			received["code_verifier"] = r.PostForm.Get("code_verifier")
			receivedContentType = r.Header.Get("Content-Type")

			w.Header().Set("Content-Type", "application/json")
			Expect(json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-token-123",
				"refresh_token": "refresh-token-123",
				"token_type":    "Bearer",
				"scope":         "openid profile",
				"expires_in":    3600,
			})).To(Succeed())
		}))
		defer tokenServer.Close()

		cfg := openAIOAuthConfig{
			TokenURL: tokenServer.URL,
			ClientID: "test-client-id",
		}

		redirectURI := "http://127.0.0.1:44444/oauth/callback"
		token, err := exchangeOpenAICodeForToken(
			context.Background(),
			tokenServer.Client(),
			cfg,
			"test-code",
			"test-verifier",
			redirectURI,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(token).NotTo(BeNil())
		Expect(token.AccessToken).To(Equal("access-token-123"))

		Expect(received["grant_type"]).To(Equal("authorization_code"))
		Expect(received["client_id"]).To(Equal("test-client-id"))
		Expect(received["code"]).To(Equal("test-code"))
		Expect(received["redirect_uri"]).To(Equal(redirectURI))
		Expect(received["code_verifier"]).To(Equal("test-verifier"))
		Expect(strings.ToLower(receivedContentType)).To(ContainSubstring("application/x-www-form-urlencoded"))
	})
})

func firstURLInOutput(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") || strings.HasPrefix(line, "http://") {
			return line
		}
	}
	return ""
}
