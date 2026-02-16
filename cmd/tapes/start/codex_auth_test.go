package startcmder

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/papercomputeco/tapes/pkg/credentials"
	"github.com/papercomputeco/tapes/pkg/start"
)

const codexOAuthAuthFixture = `{
  "OPENAI_API_KEY": null,
  "tokens": {
    "access_token": "oa-abc123",
    "refresh_token": "oa-refresh",
    "scopes": ["openid", "profile", "email", "offline_access"]
  },
  "other": "preserve-me"
}`

var _ = Describe("codex auth behavior", func() {
	var (
		tmpHome      string
		tmpConfigDir string
		tmpBinDir    string
		origHome     string
		hadHome      bool
		origPath     string
		hadPath      bool
	)

	BeforeEach(func() {
		var err error
		tmpHome, err = os.MkdirTemp("", "tapes-codex-home-*")
		Expect(err).NotTo(HaveOccurred())

		tmpConfigDir, err = os.MkdirTemp("", "tapes-codex-config-*")
		Expect(err).NotTo(HaveOccurred())

		tmpBinDir, err = os.MkdirTemp("", "tapes-codex-bin-*")
		Expect(err).NotTo(HaveOccurred())

		writeFakeCodexBinary(tmpBinDir)

		origHome, hadHome = os.LookupEnv("HOME")
		Expect(os.Setenv("HOME", tmpHome)).To(Succeed())

		origPath, hadPath = os.LookupEnv("PATH")
		newPath := tmpBinDir
		if hadPath && origPath != "" {
			newPath = newPath + string(os.PathListSeparator) + origPath
		}
		Expect(os.Setenv("PATH", newPath)).To(Succeed())
	})

	AfterEach(func() {
		if hadHome {
			Expect(os.Setenv("HOME", origHome)).To(Succeed())
		} else {
			Expect(os.Unsetenv("HOME")).To(Succeed())
		}

		if hadPath {
			Expect(os.Setenv("PATH", origPath)).To(Succeed())
		} else {
			Expect(os.Unsetenv("PATH")).To(Succeed())
		}

		Expect(os.Unsetenv("CODEX_CAPTURE_PATH")).To(Succeed())
		Expect(os.Unsetenv("CODEX_CAPTURE_OPENAI_ENV_PATH")).To(Succeed())
		Expect(os.RemoveAll(tmpHome)).To(Succeed())
		Expect(os.RemoveAll(tmpConfigDir)).To(Succeed())
		Expect(os.RemoveAll(tmpBinDir)).To(Succeed())
	})

	Describe("configureCodexAuth default/auto behavior", func() {
		It("returns success when no OpenAI key is stored and leaves auth.json unchanged", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			original := mustReadFile(authPath)

			cmder := &startCommander{configDir: tmpConfigDir}
			cleanup, err := cmder.configureCodexAuth()
			Expect(err).NotTo(HaveOccurred())

			Expect(cleanup()).To(Succeed())
			Expect(mustReadFile(authPath)).To(Equal(original))
		})

		It("patches auth.json with API key and restores on cleanup", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			original := mustReadFile(authPath)
			seedOpenAIKey(tmpConfigDir, "sk-svcacct-test")

			cmder := &startCommander{configDir: tmpConfigDir}
			cleanup, err := cmder.configureCodexAuth()
			Expect(err).NotTo(HaveOccurred())

			patched := decodeAuthMap(mustReadFile(authPath))
			Expect(patched).To(HaveKey("OPENAI_API_KEY"))
			Expect(extractAPIKey(patched)).To(Equal("sk-svcacct-test"))
			Expect(patched).NotTo(HaveKey("tokens"))
			Expect(patched).To(HaveKey("other"))

			Expect(cleanup()).To(Succeed())
			Expect(mustReadFile(authPath)).To(Equal(original))
		})

		It("does not clobber auth.json if codex rewrites it during runtime", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			seedOpenAIKey(tmpConfigDir, "sk-svcacct-test")

			cmder := &startCommander{configDir: tmpConfigDir}
			cleanup, err := cmder.configureCodexAuth()
			Expect(err).NotTo(HaveOccurred())

			authFromPatch := decodeAuthMap(mustReadFile(authPath))
			Expect(authFromPatch).NotTo(HaveKey("tokens"))
			Expect(extractAPIKey(authFromPatch)).To(Equal("sk-svcacct-test"))

			rewrittenByCodex := []byte(`{
  "OPENAI_API_KEY": null,
  "tokens": {
    "access_token": "oa-new",
    "refresh_token": "oa-refresh-new",
    "scopes": ["openid", "profile", "email", "offline_access"]
  },
  "other": "preserve-me"
}`)
			Expect(os.WriteFile(authPath, rewrittenByCodex, 0o600)).To(Succeed())

			Expect(cleanup()).To(Succeed())
			Expect(mustReadFile(authPath)).To(Equal(rewrittenByCodex))
		})

		It("is a no-op when ~/.codex/auth.json is missing", func() {
			seedOpenAIKey(tmpConfigDir, "sk-svcacct-test")

			cmder := &startCommander{configDir: tmpConfigDir}
			cleanup, err := cmder.configureCodexAuth()
			Expect(err).NotTo(HaveOccurred())
			Expect(cleanup()).To(Succeed())
		})
	})

	Describe("NewStartCmd codex auth mode flag contract", func() {
		It("exposes --codex-auth-mode with default auto", func() {
			cmd := NewStartCmd()
			f := cmd.Flags().Lookup("codex-auth-mode")
			Expect(f).NotTo(BeNil())
			Expect(f.DefValue).To(Equal("auto"))
		})

		It("rejects invalid codex auth mode values", func() {
			cmd := NewStartCmd()
			err := cmd.Flags().Set("codex-auth-mode", "bad-mode")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("auto"))
			Expect(err.Error()).To(ContainSubstring("api-key"))
			Expect(err.Error()).To(ContainSubstring("oauth"))
		})
	})

	Describe("codex auth mode execution behavior", func() {
		var pingServer *httptest.Server

		BeforeEach(func() {
			pingServer = newPingServer()
			seedHealthyDaemonState(tmpConfigDir, pingServer.URL)
		})

		AfterEach(func() {
			pingServer.Close()
		})

		It("auto mode falls back to OAuth when no key is stored", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			original := mustReadFile(authPath)

			captured, capturedOK, err := runStartCodexWithMode(tmpConfigDir, "auto")
			Expect(err).NotTo(HaveOccurred())
			Expect(capturedOK).To(BeTrue())

			authAtRuntime := decodeAuthMap(captured)
			Expect(authAtRuntime).To(HaveKey("tokens"))
			Expect(extractAPIKey(authAtRuntime)).To(BeEmpty())
			Expect(mustReadFile(authPath)).To(Equal(original))
		})

		It("auto mode prefers stored API key when present", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			original := mustReadFile(authPath)
			seedOpenAIKey(tmpConfigDir, "sk-svcacct-test")

			captured, capturedOK, err := runStartCodexWithMode(tmpConfigDir, "auto")
			Expect(err).NotTo(HaveOccurred())
			Expect(capturedOK).To(BeTrue())

			authAtRuntime := decodeAuthMap(captured)
			Expect(extractAPIKey(authAtRuntime)).To(Equal("sk-svcacct-test"))
			Expect(authAtRuntime).NotTo(HaveKey("tokens"))
			Expect(mustReadFile(authPath)).To(Equal(original))
		})

		It("api-key mode errors when no key is stored", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			original := mustReadFile(authPath)

			_, capturedOK, err := runStartCodexWithMode(tmpConfigDir, "api-key")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("tapes auth openai"))
			Expect(capturedOK).To(BeFalse())
			Expect(mustReadFile(authPath)).To(Equal(original))
		})

		It("oauth mode skips API key patching even when key is present", func() {
			authPath := writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			original := mustReadFile(authPath)
			seedOpenAIKey(tmpConfigDir, "sk-svcacct-test")

			captured, capturedOK, err := runStartCodexWithMode(tmpConfigDir, "oauth")
			Expect(err).NotTo(HaveOccurred())
			Expect(capturedOK).To(BeTrue())

			authAtRuntime := decodeAuthMap(captured)
			Expect(authAtRuntime).To(HaveKey("tokens"))
			Expect(extractAPIKey(authAtRuntime)).To(BeEmpty())
			Expect(mustReadFile(authPath)).To(Equal(original))
		})

		It("oauth mode does not inject OPENAI_API_KEY into codex env", func() {
			writeCodexAuthFile(tmpHome, []byte(codexOAuthAuthFixture))
			seedOpenAIKey(tmpConfigDir, "sk-svcacct-test")

			_, capturedOK, openAIEnv, err := runStartCodexWithModeAndCaptureOpenAIEnv(tmpConfigDir, "oauth")
			Expect(err).NotTo(HaveOccurred())
			Expect(capturedOK).To(BeTrue())
			Expect(openAIEnv).To(BeEmpty())
		})
	})
})

func writeFakeCodexBinary(binDir string) {
	scriptPath := filepath.Join(binDir, "codex")
	script := `#!/bin/sh
set -eu
auth_path="${HOME}/.codex/auth.json"
if [ -n "${CODEX_CAPTURE_PATH:-}" ] && [ -f "${auth_path}" ]; then
  cp "${auth_path}" "${CODEX_CAPTURE_PATH}"
fi
if [ -n "${CODEX_CAPTURE_OPENAI_ENV_PATH:-}" ]; then
  printf '%s' "${OPENAI_API_KEY:-}" > "${CODEX_CAPTURE_OPENAI_ENV_PATH}"
fi
exit 0
`
	Expect(os.WriteFile(scriptPath, []byte(script), 0o755)).To(Succeed())
}

func writeCodexAuthFile(home string, data []byte) string {
	authPath := filepath.Join(home, ".codex", "auth.json")
	Expect(os.MkdirAll(filepath.Dir(authPath), 0o755)).To(Succeed())
	Expect(os.WriteFile(authPath, data, 0o600)).To(Succeed())
	return authPath
}

func seedOpenAIKey(configDir, apiKey string) {
	mgr, err := credentials.NewManager(configDir)
	Expect(err).NotTo(HaveOccurred())
	Expect(mgr.SetKey("openai", apiKey)).To(Succeed())
}

func newPingServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func seedHealthyDaemonState(configDir, serverURL string) {
	manager, err := start.NewManager(configDir)
	Expect(err).NotTo(HaveOccurred())
	Expect(manager.SaveState(&start.State{
		DaemonPID: os.Getpid(),
		ProxyURL:  serverURL,
		APIURL:    serverURL,
	})).To(Succeed())
}

func runStartCodexWithMode(configDir, mode string) ([]byte, bool, error) {
	capturePath := filepath.Join(configDir, "captured-auth.json")
	Expect(os.Setenv("CODEX_CAPTURE_PATH", capturePath)).To(Succeed())

	cmd := NewStartCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.PersistentFlags().Bool("debug", false, "Enable debug logging")
	cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
	cmd.SetArgs([]string{"codex", "--config-dir", configDir, "--codex-auth-mode", mode})

	err := cmd.Execute()

	captured, readErr := os.ReadFile(capturePath)
	if readErr != nil {
		return nil, false, err
	}

	return captured, true, err
}

func runStartCodexWithModeAndCaptureOpenAIEnv(configDir, mode string) ([]byte, bool, string, error) {
	capturePath := filepath.Join(configDir, "captured-auth.json")
	captureOpenAIEnvPath := filepath.Join(configDir, "captured-openai-env.txt")
	Expect(os.Setenv("CODEX_CAPTURE_PATH", capturePath)).To(Succeed())
	Expect(os.Setenv("CODEX_CAPTURE_OPENAI_ENV_PATH", captureOpenAIEnvPath)).To(Succeed())

	cmd := NewStartCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.PersistentFlags().Bool("debug", false, "Enable debug logging")
	cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
	cmd.SetArgs([]string{"codex", "--config-dir", configDir, "--codex-auth-mode", mode})

	err := cmd.Execute()

	captured, readErr := os.ReadFile(capturePath)
	if readErr != nil {
		return nil, false, "", err
	}

	openAIEnv, readOpenAIEnvErr := os.ReadFile(captureOpenAIEnvPath)
	if readOpenAIEnvErr != nil {
		return captured, true, "", err
	}

	return captured, true, string(openAIEnv), err
}

func decodeAuthMap(data []byte) map[string]json.RawMessage {
	var auth map[string]json.RawMessage
	Expect(json.Unmarshal(data, &auth)).To(Succeed())
	return auth
}

func extractAPIKey(auth map[string]json.RawMessage) string {
	raw, ok := auth["OPENAI_API_KEY"]
	if !ok || strings.EqualFold(strings.TrimSpace(string(raw)), "null") {
		return ""
	}

	var key string
	Expect(json.Unmarshal(raw, &key)).To(Succeed())
	return key
}

func mustReadFile(path string) []byte {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return data
}
