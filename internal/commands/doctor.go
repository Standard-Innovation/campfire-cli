package commands

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Standard-Innovation/campfire-cli/internal/client"
	"github.com/Standard-Innovation/campfire-cli/internal/harness"
	"github.com/Standard-Innovation/campfire-cli/internal/render"
	"github.com/spf13/cobra"
)

// doctorCheck is one line of the doctor report.
type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass", "warn", "fail"
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Short:   "Check configuration, connectivity and credentials",
	Long:    "Runs checks on the CLI setup: Campfire URL, reachability, credential, and the agent skill.",
	Example: "$ campfire doctor\n$ campfire doctor --json",
	RunE: func(cmd *cobra.Command, args []string) error {
		checks := runDoctorChecks()

		failed := 0
		for _, c := range checks {
			if c.Status == "fail" {
				failed++
			}
		}
		summary := "All checks passed"
		if failed > 0 {
			summary = countNoun(failed, "check") + " failed"
		}

		printList(checks, render.Columns{{Header: "Check", Field: "name"}, {Header: "Status", Field: "status"}, {Header: "Detail", Field: "message"}, {Header: "Hint", Field: "hint"}}, summary, "", nil, nil)
		return nil
	},
}

func runDoctorChecks() []doctorCheck {
	var checks []doctorCheck
	resolveToken()

	checks = append(checks, doctorCheck{Name: "Version", Status: "pass", Message: currentVersion()})

	if cfg.APIURL == "" {
		checks = append(checks, doctorCheck{
			Name: "Campfire URL", Status: "fail", Message: "Not configured",
			Hint: "Run: campfire auth login --url https://chat.example.com",
		})
		return append(checks, skillCheck())
	}
	profileNote := ""
	if activeProfile != "" {
		profileNote = fmt.Sprintf(" (profile %s)", activeProfile)
	}
	checks = append(checks, doctorCheck{Name: "Campfire URL", Status: "pass", Message: cfg.APIURL + profileNote})
	checks = append(checks, reachabilityCheck(cfg.APIURL))
	checks = append(checks, credentialCheck())
	return append(checks, skillCheck())
}

// reachabilityCheck hits Campfire's unauthenticated health endpoint.
func reachabilityCheck(baseURL string) doctorCheck {
	check := doctorCheck{Name: "Reachable"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/up", nil)
	if err != nil {
		check.Status, check.Message = "fail", err.Error()
		return check
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		check.Status, check.Message = "fail", client.RedactBotKeys(err.Error())
		check.Hint = "Check the URL and your network connection"
		return check
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		check.Status, check.Message = "warn", fmt.Sprintf("/up returned %d", resp.StatusCode)
		return check
	}
	check.Status, check.Message = "pass", "Campfire is up"
	return check
}

func credentialCheck() doctorCheck {
	check := doctorCheck{Name: "Credential"}
	if cfg.Token == "" {
		check.Status, check.Message = "fail", "No token or bot key"
		check.Hint = "Run: campfire auth login --url " + cfg.APIURL
		return check
	}

	cf := getCampfire()
	if cf.IsBot() {
		if cfg.Room == "" {
			check.Status, check.Message = "warn", "Bot key set, not verified (no default room)"
			return check
		}
		if _, err := cf.Messages(cfg.Room, client.MessagesPage{}); err != nil {
			check.Status, check.Message = "fail", "Bot key rejected: "+err.Error()
			return check
		}
		check.Status, check.Message = "pass", "Bot key works for room "+cfg.Room
		return check
	}

	resp, err := cf.Me()
	if err != nil {
		check.Status, check.Message = "fail", "Token rejected: "+err.Error()
		check.Hint = "Create a new token from your Campfire profile and run: campfire auth login"
		return check
	}
	check.Status, check.Message = "pass", "Authenticated as "+userName(resp.Data)
	return check
}

func skillCheck() doctorCheck {
	if !baselineSkillInstalled() {
		return doctorCheck{Name: "Agent skill", Status: "warn", Message: "Not installed", Hint: "Run: campfire skill install"}
	}
	if harness.DetectClaude() {
		c := harness.CheckClaudeSkillLink()
		return doctorCheck{Name: "Agent skill", Status: c.Status, Message: "Installed; Claude Code: " + c.Message, Hint: c.Hint}
	}
	return doctorCheck{Name: "Agent skill", Status: "pass", Message: "Installed"}
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
