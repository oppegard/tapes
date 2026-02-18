// Package authcmder provides the auth command for storing provider credentials.
package authcmder

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/papercomputeco/tapes/pkg/credentials"
)

const authLongDesc string = `Store credentials for LLM providers.

Credentials are stored in credentials.toml in the .tapes/ directory and
automatically injected as environment variables when launching agents
via tapes start.

OpenAI OAuth credentials can also be stored with --oauth. OAuth
credentials are currently stored for future use and are not yet used by
runtime consumers.

For OpenAI, use a service account key (sk-svcacct-...) with "All"
permissions from platform.openai.com/api-keys. Personal project keys
(sk-proj-...) may lack the required API scopes for codex.

Supported providers: openai, anthropic

Examples:
  tapes auth openai              Prompt for OpenAI API key
  tapes auth openai --api-key    Force API key flow
  tapes auth openai --oauth      Authenticate OpenAI with OAuth browser flow
  tapes auth anthropic           Prompt for Anthropic API key
  tapes auth --list              List stored credentials
  tapes auth --remove openai     Remove stored OpenAI credentials
  echo $KEY | tapes auth openai  Pipe API key from stdin`

const authShortDesc string = "Store credentials for LLM providers"

func NewAuthCmd() *cobra.Command {
	var listFlag bool
	var removeFlag string
	var oauthFlag bool
	var apiKeyFlag bool

	cmd := &cobra.Command{
		Use:   "auth [provider]",
		Short: authShortDesc,
		Long:  authLongDesc,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			configDir, _ := cmd.Flags().GetString("config-dir")

			switch {
			case listFlag:
				return runList(configDir)
			case removeFlag != "":
				return runRemove(removeFlag, configDir)
			default:
				if len(args) == 0 {
					return fmt.Errorf("provider argument required\n\nSupported providers: %s",
						strings.Join(credentials.SupportedProviders(), ", "))
				}
				return runAuth(args[0], configDir, oauthFlag, apiKeyFlag)
			}
		},
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return credentials.SupportedProviders(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
	}

	cmd.Flags().BoolVar(&listFlag, "list", false, "List stored credentials")
	cmd.Flags().StringVar(&removeFlag, "remove", "", "Remove stored credentials for a provider")
	cmd.Flags().BoolVar(&oauthFlag, "oauth", false, "Use OAuth browser flow (openai only)")
	cmd.Flags().BoolVar(&apiKeyFlag, "api-key", false, "Use API key flow")

	return cmd
}

var openAIOAuthCredentialFn = func() (*credentials.OAuthCredential, error) {
	return runOpenAIOAuthFlow(context.Background(), os.Stdout, nil, loadOpenAIOAuthConfig())
}

func runAuth(provider, configDir string, oauthMode, apiKeyMode bool) error {
	provider = strings.ToLower(strings.TrimSpace(provider))

	if !credentials.IsSupportedProvider(provider) {
		return fmt.Errorf("unsupported provider: %q\n\nSupported providers: %s",
			provider, strings.Join(credentials.SupportedProviders(), ", "))
	}

	if oauthMode && apiKeyMode {
		return errors.New("flags --oauth and --api-key are mutually exclusive")
	}

	if oauthMode && provider != "openai" {
		return errors.New("flag --oauth is only supported for provider 'openai'")
	}

	mgr, err := credentials.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}

	if oauthMode {
		oauthCred, err := openAIOAuthCredentialFn()
		if err != nil {
			return fmt.Errorf("openai oauth: %w", err)
		}
		if err := mgr.SetOAuth(provider, oauthCred); err != nil {
			return err
		}

		fmt.Printf("Stored %s credentials (oauth)\n", provider)
		fmt.Println("OAuth credentials are stored for now; runtime consumption is not enabled yet.")

		return nil
	}

	apiKey, err := readAPIKey(provider)
	if err != nil {
		return err
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return errors.New("API key cannot be empty")
	}

	if err := mgr.SetKey(provider, apiKey); err != nil {
		return err
	}

	envVar := credentials.EnvVarForProvider(provider)
	fmt.Printf("Stored %s credentials (will be injected as %s)\n", provider, envVar)

	if provider == "openai" {
		if strings.HasPrefix(apiKey, "sk-proj-") {
			fmt.Println("Warning: project keys (sk-proj-...) may lack required API scopes for codex.")
			fmt.Println("Consider using a service account key (sk-svcacct-...) from platform.openai.com/api-keys.")
		}
		fmt.Println("Codex auth.json will be temporarily configured when running 'tapes start codex'.")
	}

	return nil
}

func runList(configDir string) error {
	mgr, err := credentials.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}

	providers, err := mgr.ListProviders()
	if err != nil {
		return err
	}
	creds, err := mgr.Load()
	if err != nil {
		return err
	}

	if len(providers) == 0 {
		fmt.Println("No stored credentials.")
		fmt.Printf("\nUse 'tapes auth <provider>' to store credentials.\nSupported providers: %s\n",
			strings.Join(credentials.SupportedProviders(), ", "))
		return nil
	}

	fmt.Println("Stored credentials:")
	for _, p := range providers {
		envVar := credentials.EnvVarForProvider(p)
		pc := creds.Providers[p]
		credentialType := "api_key"
		if pc.OAuth != nil && (pc.OAuth.AccessToken != "" || pc.OAuth.RefreshToken != "") {
			credentialType = "oauth"
		}
		if envVar != "" {
			fmt.Printf("  %s (%s) → %s\n", p, credentialType, envVar)
		} else {
			fmt.Printf("  %s (%s)\n", p, credentialType)
		}
	}

	return nil
}

func runRemove(provider, configDir string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))

	mgr, err := credentials.NewManager(configDir)
	if err != nil {
		return fmt.Errorf("loading credentials: %w", err)
	}

	if err := mgr.RemoveKey(provider); err != nil {
		return err
	}

	fmt.Printf("Removed %s credentials.\n", provider)

	return nil
}

// readAPIKey reads an API key from stdin. If stdin is a pipe, it reads the
// first line. Otherwise, it prompts interactively with hidden input.
func readAPIKey(provider string) (string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("checking stdin: %w", err)
	}

	// Piped input
	if (fi.Mode() & os.ModeCharDevice) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			return scanner.Text(), nil
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		return "", errors.New("no input received on stdin")
	}

	// Interactive terminal
	envVar := credentials.EnvVarForProvider(provider)
	fmt.Printf("Enter API key for %s (%s): ", provider, envVar)

	keyBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println() // newline after hidden input
	if err != nil {
		return "", fmt.Errorf("reading API key: %w", err)
	}

	return string(keyBytes), nil
}
