package commands

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Standard-Innovation/campfire-cli/internal/client"
	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/basecamp/cli/output"
	"github.com/basecamp/cli/profile"
	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

var (
	authLoginURL      string
	authLoginRoom     string
	authLoginNoVerify bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication",
	Long:  "Commands for managing Campfire credentials: personal access tokens or bot keys.",
}

var authLoginCmd = &cobra.Command{
	Use:   "login [TOKEN]",
	Short: "Save a personal access token or bot key",
	Long: `Saves a credential for a Campfire to the system keyring (or a 0600 fallback file).

The credential is either a personal access token, created from your Campfire
profile, or a bot key ("<id>-<token>", shown to admins under Bots). A token lets
you act as yourself in any room you belong to, including direct messages; a bot
key can only read and post in rooms the bot has been added to.

Pass the credential as an argument, pipe it on stdin (keeps it out of shell
history), or omit it to be prompted.`,
	Example: `$ campfire auth login --url https://chat.example.com
$ op read op://Private/Campfire/token | campfire auth login --url https://chat.example.com
$ campfire auth login 12-abcDEF123 --url https://chat.example.com --room 4`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		token, err := readLoginToken(args)
		if err != nil {
			return err
		}

		baseURL, err := normalizeBaseURL(firstNonEmpty(authLoginURL, cfgAPIURL, cfg.APIURL))
		if err != nil {
			return err
		}

		profileName := firstNonEmpty(cfgProfile, os.Getenv("CAMPFIRE_PROFILE"), profileNameForURL(baseURL))
		if nameErr := profile.ValidateName(profileName); nameErr != nil {
			return errors.NewInvalidArgsError(nameErr.Error())
		}

		result := map[string]any{
			"authenticated": true,
			"profile":       profileName,
			"base_url":      baseURL,
			"credential":    credentialKind(token),
		}

		if !authLoginNoVerify {
			verified, verifyErr := verifyCredential(baseURL, token, authLoginRoom)
			if verifyErr != nil {
				return verifyErr
			}
			for k, v := range verified {
				result[k] = v
			}
		}

		storage, err := saveProfile(profileName, baseURL, authLoginRoom, token)
		if err != nil {
			return &output.Error{Code: output.CodeAPI, Message: err.Error()}
		}
		result["storage"] = storage
		if authLoginRoom != "" {
			result["room"] = authLoginRoom
		}

		breadcrumbs := []Breadcrumb{breadcrumb("status", "campfire auth status", "Check auth status")}
		if client.IsBotKey(token) {
			breadcrumbs = append(breadcrumbs, breadcrumb("messages", "campfire message list --room <id>", "Read a room the bot belongs to"))
		} else {
			breadcrumbs = append(breadcrumbs, breadcrumb("rooms", "campfire room list", "List your rooms"))
		}

		printMutation(result, "Credential saved for "+baseURL, "", breadcrumbs)
		return nil
	},
}

var authStatusCmd = &cobra.Command{
	Use:     "status",
	Short:   "Show authentication status",
	Long:    "Shows the active profile and credential, and checks the credential against Campfire.",
	Example: "$ campfire auth status",
	RunE: func(cmd *cobra.Command, args []string) error {
		resolveToken()
		result := map[string]any{
			"authenticated": false,
			"profile":       activeProfile,
			"base_url":      cfg.APIURL,
		}
		if cfg.Token == "" || cfg.APIURL == "" {
			printDetail(result, "Not logged in", []Breadcrumb{
				breadcrumb("login", "campfire auth login --url https://chat.example.com", "Log in"),
			})
			return nil
		}

		result["credential"] = credentialKind(cfg.Token)
		result["source"] = tokenSource
		if tokenSource == tokenSourceStore {
			result["storage"] = storageKind()
		}

		cf := getCampfire()
		if cf.IsBot() {
			// Bot keys can only be checked against a room the bot belongs to.
			if cfg.Room == "" {
				result["authenticated"] = "unverified"
				printDetail(result, "Bot key configured (set a room to verify it)", nil)
				return nil
			}
			if _, err := cf.Messages(cfg.Room, client.MessagesPage{}); err != nil {
				return err
			}
			result["authenticated"] = true
			printDetail(result, "Authenticated as a bot", nil)
			return nil
		}

		resp, err := cf.Me()
		if err != nil {
			return err
		}
		result["authenticated"] = true
		result["user"] = resp.Data
		printDetail(result, "Authenticated as "+userName(resp.Data), []Breadcrumb{
			breadcrumb("rooms", "campfire room list", "List your rooms"),
		})
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:     "logout",
	Short:   "Remove saved credentials",
	Long:    "Removes the saved credential and profile for the current profile.",
	Example: "$ campfire auth logout\n$ campfire auth logout --profile work",
	RunE: func(cmd *cobra.Command, args []string) error {
		profileName := currentProfileName()
		if profileName == "" {
			return errors.NewInvalidArgsError("No profile configured. Use --profile to choose which profile to log out")
		}

		var cleanupErrors []error
		if credStore() != nil {
			if err := credsDeleteProfileToken(profileName); err != nil && !isCredentialNotFound(err) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete credential: %w", err))
			}
		}
		if profiles != nil {
			if err := profiles.Delete(profileName); err != nil && !strings.Contains(err.Error(), "not found") {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("delete profile: %w", err))
			}
		}
		if err := stderrors.Join(cleanupErrors...); err != nil {
			return &output.Error{Code: output.CodeAPI, Message: fmt.Sprintf("logout incomplete: %v", err)}
		}

		printMutation(map[string]any{
			"authenticated": false,
			"profile":       profileName,
		}, "Logged out of "+profileName, "", []Breadcrumb{
			breadcrumb("login", "campfire auth login --url https://chat.example.com", "Log in again"),
		})
		return nil
	},
}

// readLoginToken resolves the credential from the argument, piped stdin, or a prompt.
func readLoginToken(args []string) (string, error) {
	if len(args) == 1 && args[0] != "-" {
		return strings.TrimSpace(args[0]), nil
	}
	if !isTerminal(os.Stdin) || (len(args) == 1 && args[0] == "-") {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return "", errors.NewInvalidArgsError("reading token from stdin: " + err.Error())
		}
		if token := strings.TrimSpace(string(data)); token != "" {
			return token, nil
		}
		return "", errors.NewInvalidArgsError("No token provided on stdin")
	}
	if cfgAgent {
		return "", errors.NewInvalidArgsError("No token provided. Pass it as an argument or on stdin")
	}

	var token string
	err := huh.NewInput().
		Title("Personal access token or bot key").
		EchoMode(huh.EchoModePassword).
		Value(&token).
		Run()
	if err != nil || strings.TrimSpace(token) == "" {
		return "", errors.NewInvalidArgsError("No token provided")
	}
	return strings.TrimSpace(token), nil
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", errors.NewInvalidArgsError("No Campfire URL given. Pass --url https://chat.example.com")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.NewInvalidArgsError(fmt.Sprintf("Invalid Campfire URL %q", raw))
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", errors.NewInvalidArgsError("Campfire URL must use https (http is only allowed for localhost)")
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// profileNameForURL derives a profile name from the Campfire host, e.g.
// https://chat.example.com → chat-example-com.
func profileNameForURL(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "default"
	}
	name := strings.NewReplacer(".", "-", ":", "-").Replace(u.Host)
	if profile.ValidateName(name) != nil {
		return "default"
	}
	return name
}

func credentialKind(token string) string {
	if client.IsBotKey(token) {
		return "bot_key"
	}
	return "access_token"
}

// verifyCredential checks a credential before saving it. Tokens are checked
// against the profile endpoint; bot keys only when a room is known.
func verifyCredential(baseURL, token, room string) (map[string]any, error) {
	cf, c := client.NewCampfire(baseURL, token)
	c.Verbose = cfgVerbose
	c.UserAgent = "campfire-cli/" + currentVersion()

	if cf.IsBot() {
		if room == "" {
			return map[string]any{"verified": false}, nil
		}
		if _, err := cf.Messages(room, client.MessagesPage{}); err != nil {
			return nil, err
		}
		return map[string]any{"verified": true}, nil
	}

	resp, err := cf.Me()
	if err != nil {
		return nil, err
	}
	return map[string]any{"verified": true, "user": resp.Data}, nil
}

// saveProfile writes the profile and its credential, replacing any existing
// profile of the same name. Returns where the credential was stored.
func saveProfile(name, baseURL, room, token string) (string, error) {
	if profiles == nil || credStore() == nil {
		return "", fmt.Errorf("credential storage is unavailable")
	}

	p := &profile.Profile{Name: name, BaseURL: baseURL}
	if room != "" {
		raw, _ := json.Marshal(room)
		p.Extra = map[string]json.RawMessage{"room": raw}
	}

	_, defaultName, err := profiles.List()
	if err != nil {
		return "", err
	}
	if err := profiles.Delete(name); err != nil && !strings.Contains(err.Error(), "not found") {
		return "", err
	}
	if err := profiles.Create(p); err != nil {
		return "", err
	}
	if defaultName == "" || defaultName == name {
		if err := profiles.SetDefault(name); err != nil {
			return "", err
		}
	}

	if err := credsSaveProfileToken(name, token); err != nil {
		return "", fmt.Errorf("saving credential: %w", err)
	}
	return storageKind(), nil
}

func storageKind() string {
	if creds != nil && creds.UsingKeyring() {
		return "keyring"
	}
	return "file"
}

func isCredentialNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such file")
}

func userName(data any) string {
	if m, ok := data.(map[string]any); ok {
		if name, ok := m["name"].(string); ok && name != "" {
			return name
		}
	}
	return "unknown user"
}

func init() {
	rootCmd.AddCommand(authCmd)

	authLoginCmd.Flags().StringVar(&authLoginURL, "url", "", "Campfire URL, e.g. https://chat.example.com")
	authLoginCmd.Flags().StringVar(&authLoginRoom, "room", "", "Default room ID for this profile")
	authLoginCmd.Flags().BoolVar(&authLoginNoVerify, "no-verify", false, "Save without checking the credential against Campfire")

	authCmd.AddCommand(authLoginCmd)
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
}
