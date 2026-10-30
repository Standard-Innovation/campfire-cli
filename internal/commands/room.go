package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/Standard-Innovation/campfire-cli/internal/render"
	"github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
)

var roomCmd = &cobra.Command{
	Use:   "room",
	Short: "List rooms",
	Long:  "Commands for finding Campfire rooms, including direct messages.",
}

var roomListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your rooms",
	Long: `Lists the rooms you belong to: open and closed rooms and direct messages.

Each room includes a display_name: its name, or for a direct message the names
of the other people in it. Needs a personal access token.`,
	Example: "$ campfire room list\n$ campfire room list --jq '.data[] | select(.type == \"direct\")'",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		rooms, err := fetchRooms()
		if err != nil {
			return err
		}

		cols := render.Columns{
			{Header: "ID", Field: "id"},
			{Header: "Name", Field: "display_name"},
			{Header: "Type", Field: "type"},
			{Header: "Involvement", Field: "involvement"},
		}
		printList(rooms, cols, countNoun(len(rooms), "room"), nil, []Breadcrumb{
			breadcrumb("messages", "campfire message list --room <id>", "Read a room"),
			breadcrumb("post", "campfire message post --room <id> \"Hello\"", "Post to a room"),
		})
		return nil
	},
}

// fetchRooms lists the person's rooms, adding a display_name to each.
func fetchRooms() ([]any, error) {
	cf := getCampfire()
	resp, err := cf.Rooms()
	if err != nil {
		return nil, err
	}
	me, err := cf.Me()
	if err != nil {
		return nil, err
	}
	myID := idString(toMap(me.Data)["id"])
	rooms, _ := resp.Data.([]any)
	for _, r := range rooms {
		if room, ok := r.(map[string]any); ok {
			room["display_name"] = roomDisplayName(room, myID)
		}
	}
	return rooms, nil
}

// roomDisplayName is the room's name, or for a direct message the names of the
// people in it other than you.
func roomDisplayName(room map[string]any, myID string) string {
	if name, ok := room["name"].(string); ok && name != "" {
		return name
	}
	users, _ := room["users"].([]any)
	names := make([]string, 0, len(users))
	for _, u := range users {
		user, ok := u.(map[string]any)
		if !ok || idString(user["id"]) == myID {
			continue
		}
		if name, ok := user["name"].(string); ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "Room " + idString(room["id"])
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

var (
	roomIDPattern  = regexp.MustCompile(`^[0-9]+$`)
	roomURLPattern = regexp.MustCompile(`/rooms/([0-9]+)`)
)

// resolveRoom turns a room flag into a room ID. It accepts an ID, a room or
// message URL, or a display name: a room's name, or the other person's name for
// a 1:1 direct message. Falls back to the configured default room.
func resolveRoom(arg string) (string, error) {
	arg = strings.TrimSpace(firstNonEmpty(arg, cfg.Room))
	switch {
	case arg == "":
		return "", errors.NewInvalidArgsError("No room given. Pass --room (an ID, URL or name) or set CAMPFIRE_ROOM")
	case roomIDPattern.MatchString(arg):
		return arg, nil
	}
	if m := roomURLPattern.FindStringSubmatch(arg); m != nil {
		return m[1], nil
	}

	if getCampfire().IsBot() {
		return "", errors.NewInvalidArgsError(fmt.Sprintf("Bot keys need a room ID or URL, not a name (%q)", arg))
	}
	rooms, err := fetchRooms()
	if err != nil {
		return "", err
	}

	var matches []map[string]any
	for _, r := range rooms {
		room, ok := r.(map[string]any)
		if ok && roomMatches(room, arg) {
			matches = append(matches, room)
		}
	}
	switch len(matches) {
	case 0:
		return "", output.ErrNotFoundHint("room", arg, "Run 'campfire room list' to see your rooms")
	case 1:
		return idString(matches[0]["id"]), nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = fmt.Sprintf("%v (%s)", m["display_name"], idString(m["id"]))
		}
		return "", output.ErrAmbiguous("room", names)
	}
}

// roomMatches matches a room by display name, so "Sam" finds your 1:1 with Sam.
func roomMatches(room map[string]any, name string) bool {
	display, _ := room["display_name"].(string)
	return strings.EqualFold(display, name)
}

func init() {
	rootCmd.AddCommand(roomCmd)
	roomCmd.AddCommand(roomListCmd)
}
