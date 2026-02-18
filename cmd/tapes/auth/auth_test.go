package authcmder

import (
	"bytes"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/papercomputeco/tapes/pkg/credentials"
)

var _ = Describe("Auth Command", func() {
	var tmpDir string

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "auth-test-*")
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Describe("NewAuthCmd", func() {
		It("creates a command with expected properties", func() {
			cmd := NewAuthCmd()
			Expect(cmd.Use).To(Equal("auth [provider]"))
			Expect(cmd.Short).NotTo(BeEmpty())
		})

		It("has --list flag", func() {
			cmd := NewAuthCmd()
			flag := cmd.Flags().Lookup("list")
			Expect(flag).NotTo(BeNil())
		})

		It("has --remove flag", func() {
			cmd := NewAuthCmd()
			flag := cmd.Flags().Lookup("remove")
			Expect(flag).NotTo(BeNil())
		})

		It("has --oauth flag", func() {
			cmd := NewAuthCmd()
			flag := cmd.Flags().Lookup("oauth")
			Expect(flag).NotTo(BeNil())
		})

		It("has --api-key flag", func() {
			cmd := NewAuthCmd()
			flag := cmd.Flags().Lookup("api-key")
			Expect(flag).NotTo(BeNil())
		})
	})

	Describe("--list flag", func() {
		It("shows no credentials when none stored", func() {
			cmd := NewAuthCmd()
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.SetArgs([]string{"--list", "--config-dir", tmpDir})

			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")

			err := cmd.Execute()
			Expect(err).NotTo(HaveOccurred())
		})

		It("lists stored credentials", func() {
			mgr, err := credentials.NewManager(tmpDir)
			Expect(err).NotTo(HaveOccurred())
			err = mgr.SetKey("openai", "sk-test")
			Expect(err).NotTo(HaveOccurred())

			cmd := NewAuthCmd()
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
			cmd.SetArgs([]string{"--list", "--config-dir", tmpDir})

			err = cmd.Execute()
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("--remove flag", func() {
		It("removes stored credentials", func() {
			mgr, err := credentials.NewManager(tmpDir)
			Expect(err).NotTo(HaveOccurred())
			err = mgr.SetKey("openai", "sk-test")
			Expect(err).NotTo(HaveOccurred())

			cmd := NewAuthCmd()
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
			cmd.SetArgs([]string{"--remove", "openai", "--config-dir", tmpDir})

			err = cmd.Execute()
			Expect(err).NotTo(HaveOccurred())

			key, err := mgr.GetKey("openai")
			Expect(err).NotTo(HaveOccurred())
			Expect(key).To(BeEmpty())
		})
	})

	Describe("provider argument validation", func() {
		It("returns error when no provider given", func() {
			cmd := NewAuthCmd()
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
			cmd.SetArgs([]string{})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("provider argument required"))
		})

		It("returns error for unsupported provider", func() {
			cmd := NewAuthCmd()
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
			cmd.SetIn(bytes.NewBufferString("sk-test\n"))
			cmd.SetArgs([]string{"ollama", "--config-dir", tmpDir})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unsupported provider"))
		})

		It("returns error when --oauth and --api-key are both provided", func() {
			cmd := NewAuthCmd()
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
			cmd.SetArgs([]string{"openai", "--oauth", "--api-key", "--config-dir", tmpDir})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
		})

		It("returns error for anthropic --oauth", func() {
			cmd := NewAuthCmd()
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")
			cmd.SetArgs([]string{"anthropic", "--oauth", "--config-dir", tmpDir})

			err := cmd.Execute()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("only supported for provider 'openai'"))
		})
	})

	Describe("--api-key behavior", func() {
		It("stores API key and clears existing OAuth credentials", func() {
			mgr, err := credentials.NewManager(tmpDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(mgr.SetOAuth("openai", &credentials.OAuthCredential{
				AccessToken:  "oauth-access",
				RefreshToken: "oauth-refresh",
			})).To(Succeed())

			cmd := NewAuthCmd()
			cmd.PersistentFlags().String("config-dir", "", "Override path to .tapes/ config directory")

			originalStdin := os.Stdin
			reader, writer, err := os.Pipe()
			Expect(err).NotTo(HaveOccurred())
			_, err = writer.WriteString("sk-replaced\n")
			Expect(err).NotTo(HaveOccurred())
			Expect(writer.Close()).To(Succeed())
			os.Stdin = reader
			defer func() {
				os.Stdin = originalStdin
				_ = reader.Close()
			}()

			cmd.SetArgs([]string{"openai", "--api-key", "--config-dir", tmpDir})
			err = cmd.Execute()
			Expect(err).NotTo(HaveOccurred())

			key, err := mgr.GetKey("openai")
			Expect(err).NotTo(HaveOccurred())
			Expect(key).To(Equal("sk-replaced"))

			oauth, err := mgr.GetOAuth("openai")
			Expect(err).NotTo(HaveOccurred())
			Expect(oauth).To(BeNil())
		})
	})

	Describe("shell completion", func() {
		It("provides provider name completions", func() {
			cmd := NewAuthCmd()
			completions, directive := cmd.ValidArgsFunction(cmd, []string{}, "")
			Expect(completions).To(ConsistOf("openai", "anthropic"))
			Expect(directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))
		})

		It("provides no completions after first arg", func() {
			cmd := NewAuthCmd()
			completions, directive := cmd.ValidArgsFunction(cmd, []string{"openai"}, "")
			Expect(completions).To(BeNil())
			Expect(directive).To(Equal(cobra.ShellCompDirectiveNoFileComp))
		})
	})
})
