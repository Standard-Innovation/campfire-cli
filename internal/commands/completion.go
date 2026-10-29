package commands

import (
	"fmt"
	"os"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion scripts",
	Long: `Generate shell completion scripts for campfire.

To load completions:

Bash:
  $ source <(campfire completion bash)
  # To load completions for each session, execute once:
  # Linux:
  $ campfire completion bash > /etc/bash_completion.d/campfire
  # macOS:
  $ campfire completion bash > $(brew --prefix)/etc/bash_completion.d/campfire

Zsh:
  $ source <(campfire completion zsh)
  # To load completions for each session, execute once:
  $ campfire completion zsh > "${fpath[1]}/_campfire"

Fish:
  $ campfire completion fish | source
  # To load completions for each session, execute once:
  $ campfire completion fish > ~/.config/fish/completions/campfire.fish

PowerShell:
  PS> campfire completion powershell | Out-String | Invoke-Expression
  # To load completions for each session, add to your profile:
  PS> campfire completion powershell > campfire.ps1 && . campfire.ps1
`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE: func(cmd *cobra.Command, args []string) error {
		if cfgJQ != "" {
			return errors.ErrJQNotSupported("completion script generation")
		}
		var err error
		switch args[0] {
		case "bash":
			err = cmd.Root().GenBashCompletion(os.Stdout)
		case "zsh":
			err = cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			err = cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			err = cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		}
		if err != nil {
			return &output.Error{Code: output.CodeAPI, Message: fmt.Sprintf("generating %s completions: %v", args[0], err)}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(completionCmd)
}
