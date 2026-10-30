// Package commands implements CLI commands for the Campfire CLI.
package commands

import (
	"bytes"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Standard-Innovation/campfire-cli/internal/client"
	"github.com/Standard-Innovation/campfire-cli/internal/config"
	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/Standard-Innovation/campfire-cli/internal/render"
	"github.com/basecamp/cli/credstore"
	"github.com/basecamp/cli/output"
	"github.com/basecamp/cli/profile"
	"github.com/itchyny/gojq"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// Breadcrumb is a type alias for output.Breadcrumb.
type Breadcrumb = output.Breadcrumb

var (
	// Global flags
	cfgToken    string
	cfgProfile  string
	cfgAPIURL   string
	cfgVerbose  bool
	cfgJSON     bool
	cfgQuiet    bool
	cfgIDsOnly  bool
	cfgCount    bool
	cfgAgent    bool
	cfgStyled   bool
	cfgMarkdown bool
	cfgLimit    int
	cfgJQ       string

	// Loaded config
	cfg *config.Config

	// Client factory (can be overridden for testing)
	clientFactory func() *client.Campfire

	// Credential store
	creds *credstore.Store

	// Profile store and the selected profile name.
	profiles      *profile.Store
	activeProfile string

	// Output writer
	out       *output.Writer
	outWriter io.Writer // raw writer for styled/markdown rendering
)

var cliVersion = "dev"

// rootCmd represents the base command.
var rootCmd = &cobra.Command{
	Use:     "campfire",
	Short:   "Campfire CLI - Command-line interface for Campfire",
	Long:    `Command-line interface for Campfire: read and post messages from the terminal or an agent.`,
	Version: "dev",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		errOutputWrite = nil
		// Early jq validation: check flag conflicts first (actionable message),
		// then parse + compile before RunE so invalid expressions are rejected
		// with no side effects.
		var jqCode *gojq.Code
		if cfgJQ != "" {
			if cfgIDsOnly {
				return errors.ErrJQConflict("--ids-only")
			}
			if cfgCount {
				return errors.ErrJQConflict("--count")
			}
			var err error
			jqCode, err = compileJQ(cfgJQ)
			if err != nil {
				return err
			}
		}

		format, err := resolveFormat()
		if err != nil {
			return &output.Error{Code: output.CodeUsage, Message: err.Error()}
		}
		var dest io.Writer = os.Stdout
		if lastResult != nil {
			dest = &testBuf
		}
		outWriter = dest
		w := dest
		if jqCode != nil {
			w = newJQWriterWithCode(dest, jqCode)
		}
		out = output.New(output.Options{Format: format, Writer: w})

		// In test mode, cfg is already set by SetTestConfig - don't overwrite
		if cfg == nil {
			cfg = config.Load()
		}

		if profiles == nil && lastResult == nil {
			if cfgPath, err := config.ConfigPath(); err == nil {
				profiles = profile.NewStore(filepath.Join(filepath.Dir(cfgPath), "config.json"))
			}
		}

		// Login creates profiles, and logout must work on a profile whose
		// credential is already gone, so neither needs one resolved up front.
		if cmd != authLoginCmd {
			if err := resolveProfile(); err != nil && cmd != authLogoutCmd {
				return &output.Error{Code: output.CodeUsage, Message: err.Error()}
			}
		}
		// --api-url flag overrides everything (including profile BaseURL)
		if cfgAPIURL != "" {
			cfg.APIURL = strings.TrimSuffix(cfgAPIURL, "/")
		}

		if os.Getenv("CAMPFIRE_DEBUG") != "" {
			cfgVerbose = true
		}
		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		if errOutputWrite != nil {
			err := errOutputWrite
			errOutputWrite = nil
			return err
		}
		if RefreshSkillsIfVersionChanged() && !IsMachineOutput() {
			fmt.Fprintf(os.Stderr, "Agent skill updated to match CLI %s\n", currentVersion())
		}
		return nil
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

// SetVersion sets the CLI version used for `--version` and `version`.
func SetVersion(v string) {
	if v == "" {
		return
	}
	cliVersion = v
	rootCmd.Version = v
}

// Execute runs the root command.
func Execute() {
	// Default to Auto — PersistentPreRunE will re-resolve from parsed flags.
	outWriter = os.Stdout
	out = output.New(output.Options{Format: output.FormatAuto, Writer: os.Stdout})
	cmd, err := rootCmd.ExecuteC()
	if err != nil {
		if format, formatErr := resolveFormat(); formatErr == nil {
			out = output.New(output.Options{Format: format, Writer: os.Stdout})
		}

		var e *output.Error
		if !stderrors.As(err, &e) {
			// Cobra-level errors (arg count, unknown flag) → usage
			e = &output.Error{Code: output.CodeUsage, Message: err.Error()}
		}

		// jq-related errors must never be fed through the jq filter.
		if errors.IsJQError(err) && cfgJQ != "" {
			format, fmtErr := resolveFormat()
			if fmtErr != nil {
				format = output.FormatJSON
				if cfgAgent || cfgQuiet {
					format = output.FormatQuiet
				}
			}
			out = output.New(output.Options{Format: format, Writer: outWriter})
		}
		if isHumanOutput() {
			printHumanError(cmd, e)
		} else {
			_ = out.Err(e)
		}
		os.Exit(e.ExitCode())
	}
}

// resolveFormat returns the output format from flags.
// Default is FormatAuto (TTY→Styled, pipe→JSON). At most one format flag may be set.
func resolveFormat() (output.Format, error) {
	n := 0
	for _, set := range []bool{cfgJSON, cfgQuiet, cfgIDsOnly, cfgCount, cfgStyled, cfgMarkdown} {
		if set {
			n++
		}
	}
	if n > 1 {
		return 0, fmt.Errorf("only one output format flag may be used at a time (--json, --quiet, --ids-only, --count, --styled, --markdown)")
	}

	if cfgAgent && cfgStyled {
		return 0, fmt.Errorf("--agent and --styled cannot be used together")
	}

	if cfgJQ != "" && (cfgStyled || cfgMarkdown || cfgIDsOnly || cfgCount) {
		return 0, fmt.Errorf("--jq filters JSON output; use it with default JSON output or --quiet, not with --styled, --markdown, --ids-only, or --count")
	}

	switch {
	case cfgQuiet:
		return output.FormatQuiet, nil
	case cfgIDsOnly:
		return output.FormatIDs, nil
	case cfgCount:
		return output.FormatCount, nil
	case cfgJSON:
		return output.FormatJSON, nil
	case cfgStyled:
		return output.FormatStyled, nil
	case cfgMarkdown:
		return output.FormatMarkdown, nil
	}

	// --jq implies JSON (or quiet for --agent); --agent alone defaults to quiet.
	if cfgAgent {
		return output.FormatQuiet, nil
	}
	if cfgJQ != "" {
		return output.FormatJSON, nil
	}

	return output.FormatAuto, nil
}

// IsMachineOutput returns true when output should be treated as machine-consumable.
// True when any machine format flag is set, --agent is set, or stdout/stdin is not a TTY.
func IsMachineOutput() bool {
	if cfgAgent || cfgJSON || cfgQuiet || cfgIDsOnly || cfgCount || cfgJQ != "" {
		return true
	}
	if !isTerminal(os.Stdout) || !isTerminal(os.Stdin) {
		return true
	}
	return false
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func isHumanOutput() bool {
	if cfgStyled || cfgMarkdown || requestedHumanOutput() {
		return true
	}
	if out != nil {
		switch out.EffectiveFormat() {
		case output.FormatStyled, output.FormatMarkdown:
			return true
		default:
			return false
		}
	}
	return !IsMachineOutput()
}

func requestedHumanOutput() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--styled" || arg == "--markdown" {
			return true
		}
	}
	return false
}

func printHumanError(cmd *cobra.Command, err error) {
	e := output.AsError(err)
	msg := strings.TrimSpace(e.Message)
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if e.Hint != "" && !strings.Contains(msg, e.Hint) {
		fmt.Fprintf(os.Stderr, "\nHint: %s\n", e.Hint)
	}
	if e.Code == output.CodeUsage && !strings.Contains(msg, "--help") {
		fmt.Fprintf(os.Stderr, "\nRun `%s` for usage.\n", usageHelpCommand(cmd))
	}
}

func usageHelpCommand(cmd *cobra.Command) string {
	if cmd == nil {
		return rootCmd.CommandPath() + " --help"
	}
	path := strings.TrimSpace(cmd.CommandPath())
	if path == "" {
		path = rootCmd.CommandPath()
	}
	return path + " --help"
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgToken, "token", "", "Personal access token or bot key")
	rootCmd.PersistentFlags().StringVar(&cfgProfile, "profile", "", "Named profile to use")
	rootCmd.PersistentFlags().StringVar(&cfgAPIURL, "api-url", "", "Campfire URL, e.g. https://chat.example.com")
	rootCmd.PersistentFlags().BoolVar(&cfgVerbose, "verbose", false, "Show request/response details")
	rootCmd.PersistentFlags().BoolVar(&cfgJSON, "json", false, "JSON envelope output")
	rootCmd.PersistentFlags().BoolVar(&cfgQuiet, "quiet", false, "Raw JSON data without envelope")
	rootCmd.PersistentFlags().BoolVar(&cfgIDsOnly, "ids-only", false, "Print one ID per line")
	rootCmd.PersistentFlags().BoolVar(&cfgCount, "count", false, "Print count of results")
	rootCmd.PersistentFlags().BoolVar(&cfgAgent, "agent", false, "Agent mode (default: quiet format, no interactive prompts)")
	rootCmd.PersistentFlags().BoolVar(&cfgStyled, "styled", false, "Styled terminal output with colors")
	rootCmd.PersistentFlags().BoolVar(&cfgMarkdown, "markdown", false, "Markdown formatted output")
	rootCmd.PersistentFlags().IntVar(&cfgLimit, "limit", 0, "Maximum number of results to display")
	rootCmd.PersistentFlags().StringVar(&cfgJQ, "jq", "", "Apply jq filter to JSON output (built-in, no external jq required; implies --json)")

	installAgentHelp()
}

// getCampfire returns a Campfire client configured from global settings.
func getCampfire() *client.Campfire {
	if clientFactory != nil {
		return clientFactory()
	}
	cf, c := client.NewCampfire(cfg.APIURL, cfg.Token)
	c.Verbose = cfgVerbose
	c.UserAgent = "campfire-cli/" + currentVersion()
	return cf
}

// credStore opens the credential store on first use. Opening it probes the
// system keyring, which can prompt on macOS, so commands that never need a
// credential (version, help, completion) don't touch it.
func credStore() *credstore.Store {
	if creds == nil && lastResult == nil {
		fallbackDir := ""
		if cfgPath, err := config.ConfigPath(); err == nil {
			fallbackDir = filepath.Join(filepath.Dir(cfgPath), "credentials")
		}
		creds = credstore.NewStore(credstore.StoreOptions{
			ServiceName:   "campfire",
			DisableEnvVar: "CAMPFIRE_NO_KEYRING",
			FallbackDir:   fallbackDir,
		})
	}
	return creds
}

// requireAuth checks that we have a Campfire URL and a credential configured.
func requireAuth() error {
	resolveToken()
	if cfg.APIURL == "" {
		return errors.NewInvalidArgsError("No Campfire URL configured. Run 'campfire auth login --url https://chat.example.com' or set CAMPFIRE_API_URL")
	}
	if cfg.Token == "" {
		return errors.NewAuthError("No access token configured")
	}
	return nil
}

// CommandResult holds the result of a command execution for testing.
type CommandResult struct {
	Response *output.Response
}

// lastResult stores the last command result (for testing)
var lastResult *CommandResult

// testBuf captures output for test mode
var testBuf bytes.Buffer

// lastRawOutput holds the raw output from the last command (before buffer reset).
var lastRawOutput string

// errOutputWrite stores the first output rendering/writer error from the current command.
var errOutputWrite error

func recordOutputError(err error) {
	if err != nil && errOutputWrite == nil {
		errOutputWrite = err
	}
}

func writeOutputString(s string) {
	_, err := io.WriteString(outWriter, s)
	recordOutputError(err)
}

// captureResponse parses the writer buffer into lastResult after each shim call.
func captureResponse() {
	if lastResult == nil {
		return
	}
	lastRawOutput = testBuf.String()
	lastResult.Response = nil
	var resp output.Response
	if json.Unmarshal(testBuf.Bytes(), &resp) == nil {
		lastResult.Response = &resp
	}
	testBuf.Reset()
}

// printSuccess prints a success response.
func printSuccess(data any) {
	switch out.EffectiveFormat() {
	case output.FormatStyled:
		writeOutputString(renderHumanData(data, false))
	case output.FormatMarkdown:
		writeOutputString(renderHumanData(data, true))
	default:
		recordOutputError(out.OK(data))
	}
	captureResponse()
}

// breadcrumb creates a single breadcrumb.
func breadcrumb(action, cmd, description string) Breadcrumb {
	return Breadcrumb{Action: action, Cmd: cmd, Description: description}
}

// checkLimitAll validates that --limit and --all are not both set.
func checkLimitAll(all bool) error {
	if cfgLimit > 0 && all {
		return errors.NewInvalidArgsError("--limit and --all cannot be used together")
	}
	return nil
}

// truncateData applies --limit client-side truncation to a slice.
// Returns the (possibly truncated) data and the original count.
func truncateData(data any) (any, int) {
	v := reflect.ValueOf(data)
	if v.Kind() != reflect.Slice {
		return data, 0
	}
	originalCount := v.Len()
	if cfgLimit > 0 && originalCount > cfgLimit {
		return v.Slice(0, cfgLimit).Interface(), originalCount
	}
	return data, originalCount
}

// printList renders list data with format-aware dispatch. Applies --limit truncation.
func printList(data any, cols render.Columns, summary string, context map[string]any, breadcrumbs []Breadcrumb) {
	data, originalCount := truncateData(data)
	var notice string
	if cfgLimit > 0 && originalCount > cfgLimit {
		notice = fmt.Sprintf("Showing %d of %d results", cfgLimit, originalCount)
	}

	switch out.EffectiveFormat() {
	case output.FormatStyled:
		body := render.StyledList(toMaps(data), cols, summary)
		writeOutputString(appendHumanSections(body, notice, "", breadcrumbs, false))
	case output.FormatMarkdown:
		body := render.MarkdownList(toMaps(data), cols, summary)
		writeOutputString(appendHumanSections(body, notice, "", breadcrumbs, true))
	default:
		opts := []output.ResponseOption{output.WithBreadcrumbs(breadcrumbs...)}
		if summary != "" {
			opts = append(opts, output.WithSummary(summary))
		}
		if notice != "" {
			opts = append(opts, output.WithNotice(notice))
		}
		for key, value := range context {
			opts = append(opts, output.WithContext(key, value))
		}
		recordOutputError(out.OK(data, opts...))
	}
	captureResponse()
}

// printDetail renders a single object with format-aware dispatch.
func printDetail(data any, summary string, breadcrumbs []Breadcrumb) {
	switch out.EffectiveFormat() {
	case output.FormatStyled:
		body := render.StyledDetail(toMap(data), summary)
		writeOutputString(appendHumanSections(body, "", "", breadcrumbs, false))
	case output.FormatMarkdown:
		body := render.MarkdownDetail(toMap(data), summary)
		writeOutputString(appendHumanSections(body, "", "", breadcrumbs, true))
	default:
		opts := []output.ResponseOption{output.WithBreadcrumbs(breadcrumbs...)}
		if summary != "" {
			opts = append(opts, output.WithSummary(summary))
		}
		recordOutputError(out.OK(data, opts...))
	}
	captureResponse()
}

// printMutation renders a mutation result with format-aware dispatch.
// For styled/markdown, uses summary rendering for simple confirmations.
func printMutation(data any, summary, location string, breadcrumbs []Breadcrumb) {
	switch out.EffectiveFormat() {
	case output.FormatStyled:
		body := render.StyledSummary(toMap(data), summary)
		writeOutputString(appendHumanSections(body, "", location, breadcrumbs, false))
	case output.FormatMarkdown:
		body := render.MarkdownSummary(toMap(data), summary)
		writeOutputString(appendHumanSections(body, "", location, breadcrumbs, true))
	default:
		opts := []output.ResponseOption{output.WithBreadcrumbs(breadcrumbs...)}
		if summary != "" {
			opts = append(opts, output.WithSummary(summary))
		}
		if location != "" {
			opts = append(opts, output.WithContext("location", location))
		}
		recordOutputError(out.OK(data, opts...))
	}
	captureResponse()
}

func renderHumanData(data any, markdown bool) string {
	if m, ok := data.(map[string]any); ok || data == nil {
		if markdown {
			return render.MarkdownDetail(m, "")
		}
		return render.StyledDetail(m, "")
	}
	if maps := toMaps(data); maps != nil {
		cols := inferColumns(maps)
		if markdown {
			return render.MarkdownList(maps, cols, "")
		}
		return render.StyledList(maps, cols, "")
	}
	if m := toMap(data); m != nil {
		if markdown {
			return render.MarkdownDetail(m, "")
		}
		return render.StyledDetail(m, "")
	}
	return fmt.Sprintf("%v\n", data)
}

func appendHumanSections(body, notice, location string, breadcrumbs []Breadcrumb, markdown bool) string {
	body = strings.TrimRight(body, "\n")
	var sb strings.Builder
	if body != "" {
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	if notice != "" {
		sb.WriteString("\n")
		if markdown {
			sb.WriteString("> ")
		}
		sb.WriteString(notice)
		sb.WriteString("\n")
	}
	if location != "" {
		sb.WriteString("\n")
		if markdown {
			sb.WriteString("**Location:** `" + location + "`\n")
		} else {
			sb.WriteString("Location: " + location + "\n")
		}
	}
	if len(breadcrumbs) > 0 {
		sb.WriteString("\n")
		if markdown {
			sb.WriteString("### Next steps\n")
			for _, crumb := range breadcrumbs {
				sb.WriteString("- `" + crumb.Cmd + "`")
				if crumb.Description != "" {
					sb.WriteString(" — " + crumb.Description)
				}
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString("Next steps:\n")
			for _, crumb := range breadcrumbs {
				sb.WriteString("  " + crumb.Cmd)
				if crumb.Description != "" {
					sb.WriteString("  # " + crumb.Description)
				}
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

func inferColumns(data []map[string]any) render.Columns {
	if len(data) == 0 {
		return render.Columns{{Header: "Value", Field: "id"}}
	}

	priority := map[string]int{"id": 0, "profile": 1, "name": 2, "type": 3, "base_url": 4}

	keys := make([]string, 0, len(data[0]))
	for key := range data[0] {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, okI := priority[keys[i]]
		pj, okJ := priority[keys[j]]
		if okI && okJ {
			return pi < pj
		}
		if okI != okJ {
			return okI
		}
		return keys[i] < keys[j]
	})

	if len(keys) > 4 {
		keys = keys[:4]
	}

	cols := make(render.Columns, 0, len(keys))
	for _, key := range keys {
		cols = append(cols, render.Column{Header: humanizeFieldName(key), Field: key})
	}
	return cols
}

func humanizeFieldName(name string) string {
	parts := strings.Split(name, "_")
	for i, part := range parts {
		if part == "id" || part == "url" {
			parts[i] = strings.ToUpper(part)
			continue
		}
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		}
	}
	return strings.Join(parts, " ")
}

// toMaps converts any (expected []any of map[string]any) to []map[string]any.
func toMaps(data any) []map[string]any {
	if data == nil {
		return nil
	}
	if maps, ok := data.([]map[string]any); ok {
		return maps
	}
	if slice, ok := data.([]any); ok {
		result := make([]map[string]any, 0, len(slice))
		for _, item := range slice {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
		return result
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var result []map[string]any
	if json.Unmarshal(b, &result) == nil {
		return result
	}
	return nil
}

// toMap converts any (expected map[string]any) to map[string]any.
func toMap(data any) map[string]any {
	if m, ok := data.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		return m
	}
	return nil
}

// countNoun is "1 message" or "2 messages".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// idString formats a JSON id, which decodes as a float64: %v would print one
// of a million or more in exponent form.
func idString(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// credsSaveProfileToken JSON-encodes a credential and saves it under a
// profile-scoped key ("profile:<name>").
func credsSaveProfileToken(profileName, token string) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return credStore().Save(profile.CredentialKey(profileName, ""), data)
}

func credsLoadProfileToken(profileName string) (string, error) {
	data, err := credStore().Load(profile.CredentialKey(profileName, ""))
	if err != nil {
		return "", err
	}
	var token string
	if err := json.Unmarshal(data, &token); err != nil {
		return "", err
	}
	return token, nil
}

func credsDeleteProfileToken(profileName string) error {
	return credStore().Delete(profile.CredentialKey(profileName, ""))
}

// resolveProfile selects the active profile and applies its URL and default
// room. Environment variables keep precedence over profile settings.
func resolveProfile() error {
	if profiles == nil {
		return nil
	}
	all, defaultName, err := profiles.List()
	if err != nil {
		return err
	}
	name, err := profile.Resolve(profile.ResolveOptions{
		FlagValue:      cfgProfile,
		EnvVar:         os.Getenv("CAMPFIRE_PROFILE"),
		DefaultProfile: defaultName,
		Profiles:       all,
	})
	if err != nil || name == "" {
		return err
	}
	activeProfile = name
	p := all[name]
	if os.Getenv("CAMPFIRE_API_URL") == "" && p.BaseURL != "" {
		cfg.APIURL = strings.TrimSuffix(p.BaseURL, "/")
	}
	if os.Getenv("CAMPFIRE_ROOM") == "" {
		if room := profileRoom(p); room != "" {
			cfg.Room = room
		}
	}
	return nil
}

func profileRoom(p *profile.Profile) string {
	var room string
	if raw, ok := p.Extra["room"]; ok {
		_ = json.Unmarshal(raw, &room)
	}
	return room
}

// tokenSourceStore labels a credential loaded from the keyring or credential file.
const tokenSourceStore = "credential_store" // #nosec G101 -- a source label, not a credential

// tokenSource records where the active credential came from, for auth status.
var (
	tokenSource   string
	tokenResolved bool
)

// resolveToken applies the credential with precedence: --token flag,
// CAMPFIRE_TOKEN, the active profile's stored credential, then config files.
// It runs once, on the first command that needs a credential.
func resolveToken() {
	if tokenResolved {
		return
	}
	tokenResolved = true
	switch {
	case cfgToken != "":
		cfg.Token = cfgToken
		tokenSource = "flag"
	case os.Getenv("CAMPFIRE_TOKEN") != "":
		tokenSource = "env"
	default:
		if activeProfile != "" && credStore() != nil {
			if token, err := credsLoadProfileToken(activeProfile); err == nil && token != "" {
				cfg.Token = token
				tokenSource = tokenSourceStore
				return
			}
		}
		if cfg.Token != "" {
			tokenSource = "config"
		}
	}
}

// currentProfileName returns the profile named by flag or env, else the active one.
func currentProfileName() string {
	return firstNonEmpty(cfgProfile, os.Getenv("CAMPFIRE_PROFILE"), activeProfile)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// SetTestMode configures the commands package for testing against a fake
// Campfire. Output is captured instead of written to stdout.
func SetTestMode(baseURL, token string) *CommandResult {
	clientFactory = func() *client.Campfire {
		cf, c := client.NewCampfire(baseURL, token)
		c.Sleeper = func(time.Duration) {}
		return cf
	}
	testBuf.Reset()
	outWriter = &testBuf
	out = output.New(output.Options{Format: output.FormatJSON, Writer: &testBuf})
	cfg = &config.Config{APIURL: baseURL, Token: token}
	lastResult = &CommandResult{}
	return lastResult
}

// ResetTestMode resets all test state.
func ResetTestMode() {
	clientFactory = nil
	cfg = nil
	creds = nil
	profiles = nil
	activeProfile = ""
	tokenSource = ""
	tokenResolved = false
	lastResult = nil
	lastRawOutput = ""
	testBuf.Reset()
	cfgToken, cfgProfile, cfgAPIURL, cfgJQ = "", "", "", ""
	cfgVerbose, cfgJSON, cfgQuiet, cfgIDsOnly, cfgCount = false, false, false, false, false
	cfgAgent, cfgStyled, cfgMarkdown = false, false, false
	cfgLimit = 0
}

// TestOutput returns the raw output from the last command execution.
func TestOutput() string {
	return lastRawOutput
}

// GetRootCmd returns the root command (for testing and surface snapshots).
func GetRootCmd() *cobra.Command {
	return rootCmd
}
