package commands

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/Standard-Innovation/campfire-cli/internal/client"
	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/Standard-Innovation/campfire-cli/internal/render"
	"github.com/spf13/cobra"
)

var messageCmd = &cobra.Command{
	Use:     "message",
	Aliases: []string{"messages"},
	Short:   "Read and post messages",
	Long:    "Commands for reading and posting messages in a Campfire room.",
}

var (
	messageListRoom   string
	messageListBefore string
	messageListAfter  string
	messageListAll    bool
)

var messageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List messages in a room",
	Long: `Lists a room's messages, oldest first.

Without --before or --after, shows the latest page. --before ID pages back
through older messages and --after ID forward through newer ones. --limit N
keeps the newest N, fetching older pages as needed; --all fetches the whole
history.`,
	Example: `$ campfire message list --room 4
$ campfire message list --room Sam --limit 10
$ campfire message list --room https://chat.example.com/rooms/4 --before 1200
$ campfire message list --room 4 --jq '.data[] | "\(.creator.name): \(.body.plain_text)"'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkLimitAll(messageListAll); err != nil {
			return err
		}
		if messageListBefore != "" && messageListAfter != "" {
			return errors.NewInvalidArgsError("--before and --after cannot be used together")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		roomID, err := resolveRoom(messageListRoom)
		if err != nil {
			return err
		}

		cf := getCampfire()
		resp, err := cf.Messages(roomID, client.MessagesPage{Before: messageListBefore, After: messageListAfter})
		if err != nil {
			return err
		}
		messages, _ := resp.Data.([]any)
		next := resp.LinkNext

		// Pages are oldest first. Paging back (the default) prepends older
		// pages; paging forward with --after appends newer ones.
		forward := messageListAfter != ""
		for (messageListAll || cfgLimit > len(messages)) && next != "" {
			page, err := cf.API.Get(next)
			if err != nil {
				return err
			}
			older, _ := page.Data.([]any)
			if forward {
				messages = append(messages, older...)
			} else {
				messages = append(older, messages...)
			}
			next = page.LinkNext
		}

		notice := ""
		if cfgLimit > 0 && len(messages) > cfgLimit {
			notice = fmt.Sprintf("Showing %d of %d fetched messages", cfgLimit, len(messages))
			if forward {
				messages = messages[:cfgLimit]
			} else {
				messages = messages[len(messages)-cfgLimit:]
			}
		}

		context := map[string]any{"room_id": roomID}
		if resp.TotalCount >= 0 {
			context["total_count"] = resp.TotalCount
		}
		breadcrumbs := []Breadcrumb{
			breadcrumb("post", fmt.Sprintf("campfire message post --room %s \"...\"", roomID), "Reply in this room"),
		}
		if cursor := pageCursor(next); cursor != "" {
			context["pagination"] = map[string]any{"has_next": true, "next": cursor}
			direction := "older"
			if forward {
				direction = "newer"
			}
			breadcrumbs = append(breadcrumbs, breadcrumb("more",
				fmt.Sprintf("campfire message list --room %s %s", roomID, cursor), "Show "+direction+" messages"))
		}

		cols := render.Columns{
			{Header: "ID", Field: "id"},
			{Header: "When", Field: "created_at"},
			{Header: "From", Field: "creator.name"},
			{Header: "Message", Field: "body.plain_text"},
		}
		printList(messages, cols, fmt.Sprintf("%d messages", len(messages)), notice, context, breadcrumbs)
		return nil
	},
}

// pageCursor turns a Link rel="next" URL into the equivalent flag, so output
// never echoes URLs (which carry the bot key for bot requests).
func pageCursor(next string) string {
	if next == "" {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil {
		return ""
	}
	q := u.Query()
	if before := q.Get("before"); before != "" {
		return "--before " + before
	}
	if after := q.Get("after"); after != "" {
		return "--after " + after
	}
	return ""
}

var (
	messagePostRoom string
	messagePostBody string
)

var messagePostCmd = &cobra.Command{
	Use:   "post [BODY]",
	Short: "Post a message to a room",
	Long: `Posts a message to a room as you (with a personal access token) or as the
bot (with a bot key).

The body comes from --body, the BODY argument, or stdin when piped. The room is
an ID, a room or message URL, a room name, or the name of the other person in a
1:1 direct message.`,
	Example: `$ campfire message post --room 4 "Deploy finished"
$ campfire message post --room Sam -b "Got a minute?"
$ git log -1 --format=%s | campfire message post --room 4`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := resolveBody(messagePostBody, args)
		if err != nil {
			return err
		}
		if err = requireAuth(); err != nil {
			return err
		}
		roomID, err := resolveRoom(messagePostRoom)
		if err != nil {
			return err
		}

		cf := getCampfire()
		resp, err := cf.Post(roomID, body)
		if err != nil {
			return err
		}

		data := resp.Data
		location := ""
		if m, ok := data.(map[string]any); ok {
			location, _ = m["url"].(string)
		}
		if location == "" {
			location = resp.Location
		}
		if data == nil {
			// Bot posts answer 201 with only a Location header.
			data = map[string]any{"room_id": roomID, "url": location}
		}

		if isHumanOutput() {
			data = messageSummary(data)
		}
		printMutation(data, "Posted to room "+roomID, location, []Breadcrumb{
			breadcrumb("messages", "campfire message list --room "+roomID, "Read the room"),
		})
		return nil
	},
}

// messageSummary flattens a message for styled and markdown output, which
// otherwise print the nested body (including its HTML) as a raw map.
func messageSummary(data any) any {
	m := toMap(data)
	if m == nil || m["body"] == nil {
		return data
	}
	summary := map[string]any{"id": m["id"], "url": m["url"], "created_at": m["created_at"]}
	if body := toMap(m["body"]); body != nil {
		summary["message"] = body["plain_text"]
	}
	if creator := toMap(m["creator"]); creator != nil {
		summary["from"] = creator["name"]
	}
	return summary
}

// resolveBody follows the shared input conventions: flag, then positional
// argument, then piped stdin.
func resolveBody(flag string, args []string) (string, error) {
	if flag != "" && len(args) > 0 {
		return "", errors.NewInvalidArgsError("--body and positional argument are mutually exclusive")
	}
	body := flag
	if body == "" && len(args) > 0 {
		body = args[0]
	}
	if body == "" && !isTerminal(os.Stdin) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", errors.NewInvalidArgsError("reading message from stdin: " + err.Error())
		}
		body = string(data)
	}
	body = strings.TrimRight(body, "\r\n")
	if strings.TrimSpace(body) == "" {
		return "", errors.NewInvalidArgsError("No message given. Pass it as an argument, with --body, or on stdin")
	}
	return body, nil
}

func init() {
	rootCmd.AddCommand(messageCmd)

	messageListCmd.Flags().StringVarP(&messageListRoom, "room", "r", "", "Room ID, URL or name (default: CAMPFIRE_ROOM)")
	messageListCmd.Flags().StringVar(&messageListBefore, "before", "", "Show messages before this message ID")
	messageListCmd.Flags().StringVar(&messageListAfter, "after", "", "Show messages after this message ID")
	messageListCmd.Flags().BoolVar(&messageListAll, "all", false, "Fetch the room's whole history")
	messageCmd.AddCommand(messageListCmd)

	messagePostCmd.Flags().StringVarP(&messagePostRoom, "room", "r", "", "Room ID, URL or name (default: CAMPFIRE_ROOM)")
	messagePostCmd.Flags().StringVarP(&messagePostBody, "body", "b", "", "Message text")
	messageCmd.AddCommand(messagePostCmd)
}
