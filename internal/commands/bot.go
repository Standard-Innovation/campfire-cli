package commands

import (
	"fmt"
	"strings"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/Standard-Innovation/campfire-cli/internal/render"
	"github.com/spf13/cobra"
)

const botForbidden = "Campfire refused: managing bots needs a write token whose owner is an administrator"

var botCmd = &cobra.Command{
	Use:     "bot",
	Aliases: []string{"bots"},
	Short:   "Manage bots",
	Long: `Commands for an administrator to list, create, rekey and delete the account's bots.

Needs a write personal access token whose owner is an administrator. A bot's key
is shown once, when the bot is created or rekeyed: store it straight into a
secret manager. Tokens cannot set a bot's webhook; add one in the web UI.`,
}

var botListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List bots",
	Long:    "Lists the account's active bots. Campfire never lists their keys.",
	Example: "$ campfire bot list",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		resp, err := getCampfire().Bots()
		if err != nil {
			return explainWriteError(err, botForbidden, "")
		}
		bots, _ := resp.Data.([]any)
		cols := render.Columns{
			{Header: "ID", Field: "id"},
			{Header: "Name", Field: "name"},
		}
		printList(bots, cols, countNoun(len(bots), "bot"), nil, []Breadcrumb{
			breadcrumb("rekey", "campfire bot rekey <id>", "Make a new key for a bot"),
			breadcrumb("delete", "campfire bot delete <id>", "Delete a bot"),
		})
		return nil
	},
}

var botCreateName string

var botCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a bot and show its key once",
	Long: `Creates a bot and prints its key. Campfire shows the key only now.

--quiet prints the key alone, so it can go straight into a secret manager.`,
	Example: `$ campfire bot create --name "Deploy Bot"
$ key=$(campfire bot create --name "Deploy Bot" --quiet)`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(botCreateName)
		if name == "" {
			return errors.NewInvalidArgsError("No bot name given. Pass --name")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		resp, err := getCampfire().CreateBot(name)
		if err != nil {
			return explainWriteError(err, botForbidden, "Campfire rejected the bot: it needs a name")
		}
		printBotKey(resp.Data, "Created bot")
		return nil
	},
}

var botRekeyCmd = &cobra.Command{
	Use:   "rekey <id>",
	Short: "Make a new key for a bot and show it once",
	Long: `Replaces a bot's key and prints the new one. The old key stops working at once,
and Campfire shows the new key only now.

--quiet prints the key alone, so it can go straight into a secret manager.`,
	Example: "$ campfire bot rekey 12\n$ key=$(campfire bot rekey 12 --quiet)",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		botID, err := requireID(args[0], "bot")
		if err != nil {
			return err
		}
		if err = requireAuth(); err != nil {
			return err
		}
		resp, err := getCampfire().RekeyBot(botID)
		if err != nil {
			return explainWriteError(err, botForbidden, "")
		}
		printBotKey(resp.Data, "New key for bot")
		return nil
	},
}

var botDeleteYes bool

var botDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Short:   "Delete a bot",
	Long:    "Deactivates a bot: its key stops working and it leaves the bot list. Asks first unless --yes or --agent.",
	Example: "$ campfire bot delete 12\n$ campfire bot delete 12 --yes",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		botID, err := requireID(args[0], "bot")
		if err != nil {
			return err
		}
		if err = requireAuth(); err != nil {
			return err
		}
		if err = confirmDestructive(fmt.Sprintf("Delete bot %s?", botID), botDeleteYes); err != nil {
			return err
		}
		if _, err = getCampfire().DeleteBot(botID); err != nil {
			return explainWriteError(err, botForbidden, "")
		}
		printMutation(map[string]any{"id": botID, "deleted": true}, "Deleted bot "+botID, "", []Breadcrumb{
			breadcrumb("bots", "campfire bot list", "List the remaining bots"),
		})
		return nil
	},
}

// printBotKey shows a bot with its key. The key is a secret Campfire shows only
// once: --quiet prints it alone for piping into a secret manager, and human
// output says so before printing it on a line of its own.
func printBotKey(data any, summary string) {
	bot := toMap(data)
	key, _ := bot["bot_key"].(string)
	id := idString(bot["id"])
	breadcrumbs := []Breadcrumb{
		breadcrumb("room", fmt.Sprintf("campfire room create --name \"...\" --member %s", id), "Make a room for the bot"),
	}
	switch {
	case cfgQuiet && cfgJQ == "":
		writeOutputString(key + "\n")
		captureResponse()
	case isHumanOutput():
		writeOutputString(fmt.Sprintf("%s %s (%v)\n\nThis key is shown once. Store it now, in a secret manager:\n%s\n", summary, id, bot["name"], key))
		captureResponse()
	default:
		printMutation(bot, fmt.Sprintf("%s %s", summary, id), "", breadcrumbs)
	}
}

// requireID checks that an argument is a numeric ID before it goes into a path.
func requireID(arg, noun string) (string, error) {
	arg = strings.TrimSpace(arg)
	if !idPattern.MatchString(arg) {
		return "", errors.NewInvalidArgsError(fmt.Sprintf("Invalid %s ID %q: expected a number", noun, arg))
	}
	return arg, nil
}

func init() {
	rootCmd.AddCommand(botCmd)

	botCmd.AddCommand(botListCmd)

	botCreateCmd.Flags().StringVar(&botCreateName, "name", "", "Bot name")
	botCmd.AddCommand(botCreateCmd)

	botCmd.AddCommand(botRekeyCmd)

	botDeleteCmd.Flags().BoolVarP(&botDeleteYes, "yes", "y", false, "Delete without asking")
	botCmd.AddCommand(botDeleteCmd)
}
