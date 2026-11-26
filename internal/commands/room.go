package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
	"github.com/Standard-Innovation/campfire-cli/internal/render"
	"github.com/basecamp/cli/output"
	"github.com/spf13/cobra"
)

var roomCmd = &cobra.Command{
	Use:   "room",
	Short: "List, create and update rooms",
	Long:  "Commands for finding Campfire rooms, including direct messages, and for making and changing rooms.",
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
		rooms, _, err := fetchRooms()
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

var (
	roomCreateName    string
	roomCreateOpen    bool
	roomCreateMembers []string
)

var roomCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a room",
	Long: `Creates a closed room holding you and the people and bots given with --member,
or with --open an open room, which holds every person and bot on the account.

Needs a write personal access token. The account may limit making rooms to
administrators.`,
	Example: `$ campfire room create --name Deploys --member 12
$ campfire room create --name Ops --member 2 --member 3
$ campfire room create --name Announcements --open`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(roomCreateName)
		if name == "" {
			return errors.NewInvalidArgsError("No room name given. Pass --name")
		}
		if roomCreateOpen && len(roomCreateMembers) > 0 {
			return errors.NewInvalidArgsError("--open and --member cannot be used together: an open room holds everyone")
		}
		userIDs, err := parseMemberIDs(roomCreateMembers)
		if err != nil {
			return err
		}
		if err = requireAuth(); err != nil {
			return err
		}
		resp, err := getCampfire().CreateRoom(name, roomCreateOpen, userIDs)
		if err != nil {
			return explainRefusal(err,
				"Campfire refused: making a room needs a write token, and the account may limit it to administrators",
				"Campfire rejected the room: check the name and the member IDs")
		}
		printRoomMutation(resp.Data, "Created room")
		return nil
	},
}

var (
	roomUpdateName    string
	roomUpdateMembers []string
	roomUpdateYes     bool
)

var roomUpdateCmd = &cobra.Command{
	Use:   "update <room>",
	Short: "Rename a closed room and replace its members",
	Long: `Replaces a closed room's members with the people and bots given with --member,
and renames it with --name. Campfire always keeps you in the room.

The room is an ID, a room URL or a room name. --member is the whole membership,
so list everyone who should stay. Without --name the room keeps its name. Asks
first unless --yes. Needs a write personal access token owned by the room's
creator or an administrator. Open rooms are changed in the web UI.`,
	Example: `$ campfire room update 4 --member 2 --member 12
$ campfire room update Deploys --name Releases --member 2,12 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" {
			return errors.NewInvalidArgsError("No room given. Pass an ID, URL or name")
		}
		if len(roomUpdateMembers) == 0 {
			return errors.NewInvalidArgsError("No members given. Pass --member for everyone who should be in the room; it replaces the whole list")
		}
		userIDs, err := parseMemberIDs(roomUpdateMembers)
		if err != nil {
			return err
		}
		if err = requireAuth(); err != nil {
			return err
		}
		rooms, myID, err := fetchRooms()
		if err != nil {
			return err
		}
		room, err := lookupRoom(rooms, args[0])
		if err != nil {
			return err
		}
		roomID := idString(room["id"])
		if room["type"] != "closed" {
			return errors.NewInvalidArgsError(fmt.Sprintf("Room %s is %v, not closed: only closed rooms can be updated here. Change it in the web UI", roomID, room["type"]))
		}
		name := strings.TrimSpace(roomUpdateName)
		if name == "" {
			name, _ = room["name"].(string)
		}
		if err = confirmDestructive(updateQuestion(room, userIDs, myID), roomUpdateYes); err != nil {
			return err
		}
		resp, err := getCampfire().UpdateClosedRoom(roomID, name, userIDs)
		if err != nil {
			return explainRefusal(err,
				"Campfire refused: changing a room needs a write token owned by the room's creator or an administrator",
				"Campfire rejected the change: check the name and the member IDs. Open rooms are changed in the web UI")
		}
		printRoomMutation(resp.Data, "Updated room")
		return nil
	},
}

// lookupRoom finds a room, given as an ID, URL or name, in the person's rooms.
func lookupRoom(rooms []any, arg string) (map[string]any, error) {
	roomID := roomIDFromArg(arg)
	if roomID == "" {
		return matchRoomByName(rooms, arg)
	}
	for _, r := range rooms {
		if room := toMap(r); idString(room["id"]) == roomID {
			return room, nil
		}
	}
	return nil, output.ErrNotFoundHint("room", roomID, "Run 'campfire room list' to see your rooms")
}

// updateQuestion says what replacing a room's members does. Campfire lists the
// members of direct rooms only, so for other rooms it can't count who leaves.
func updateQuestion(room map[string]any, userIDs []int64, myID string) string {
	question := fmt.Sprintf("Update room %s?", idString(room["id"]))
	users, ok := room["users"].([]any)
	if !ok {
		return question + fmt.Sprintf(" Its members become the %d given and you; anyone else is removed.", len(userIDs))
	}
	keep := map[string]bool{myID: true}
	for _, id := range userIDs {
		keep[strconv.FormatInt(id, 10)] = true
	}
	removed := 0
	for _, u := range users {
		if !keep[idString(toMap(u)["id"])] {
			removed++
		}
	}
	return question + fmt.Sprintf(" This removes %s.", countNoun(removed, "member"))
}

// parseMemberIDs turns --member values, repeated or comma-separated, into user IDs.
func parseMemberIDs(members []string) ([]int64, error) {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		digits, err := requireID(member, "member")
		if err != nil {
			return nil, err
		}
		id, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return nil, errors.NewInvalidArgsError(fmt.Sprintf("Invalid member ID %q: expected a number", digits))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// printRoomMutation prints a created or updated room.
func printRoomMutation(data any, verb string) {
	room := toMap(data)
	id := idString(room["id"])
	printMutation(room, fmt.Sprintf("%s %s", verb, id), "", []Breadcrumb{
		breadcrumb("post", fmt.Sprintf("campfire message post --room %s \"...\"", id), "Post to the room"),
		breadcrumb("update", fmt.Sprintf("campfire room update %s --member <id>", id), "Change who is in the room"),
	})
}

// fetchRooms lists the person's rooms, adding a display_name to each, and
// returns the person's own user ID.
func fetchRooms() ([]any, string, error) {
	cf := getCampfire()
	resp, err := cf.Rooms()
	if err != nil {
		return nil, "", err
	}
	me, err := cf.Me()
	if err != nil {
		return nil, "", err
	}
	myID := idString(toMap(me.Data)["id"])
	rooms, _ := resp.Data.([]any)
	for _, r := range rooms {
		if room, ok := r.(map[string]any); ok {
			room["display_name"] = roomDisplayName(room, myID)
		}
	}
	return rooms, myID, nil
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

var roomURLPattern = regexp.MustCompile(`/rooms/([0-9]+)`)

// roomIDFromArg is the room ID in an ID or a room or message URL, or "" for a name.
func roomIDFromArg(arg string) string {
	arg = strings.TrimSpace(arg)
	if idPattern.MatchString(arg) {
		return arg
	}
	if m := roomURLPattern.FindStringSubmatch(arg); m != nil {
		return m[1]
	}
	return ""
}

// resolveRoom turns a room flag into a room ID. It accepts an ID, a room or
// message URL, or a display name: a room's name, or the other person's name for
// a 1:1 direct message. Falls back to the configured default room.
func resolveRoom(arg string) (string, error) {
	arg = strings.TrimSpace(firstNonEmpty(arg, cfg.Room))
	if arg == "" {
		return "", errors.NewInvalidArgsError("No room given. Pass --room (an ID, URL or name) or set CAMPFIRE_ROOM")
	}
	if roomID := roomIDFromArg(arg); roomID != "" {
		return roomID, nil
	}

	if getCampfire().IsBot() {
		return "", errors.NewInvalidArgsError(fmt.Sprintf("Bot keys need a room ID or URL, not a name (%q)", arg))
	}
	rooms, _, err := fetchRooms()
	if err != nil {
		return "", err
	}
	room, err := matchRoomByName(rooms, arg)
	if err != nil {
		return "", err
	}
	return idString(room["id"]), nil
}

// matchRoomByName finds the one room whose display name is name.
func matchRoomByName(rooms []any, name string) (map[string]any, error) {
	var matches []map[string]any
	for _, r := range rooms {
		room, ok := r.(map[string]any)
		if ok && roomMatches(room, name) {
			matches = append(matches, room)
		}
	}
	switch len(matches) {
	case 0:
		return nil, output.ErrNotFoundHint("room", name, "Run 'campfire room list' to see your rooms")
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = fmt.Sprintf("%v (%s)", m["display_name"], idString(m["id"]))
		}
		return nil, output.ErrAmbiguous("room", names)
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

	roomCreateCmd.Flags().StringVar(&roomCreateName, "name", "", "Room name")
	roomCreateCmd.Flags().BoolVar(&roomCreateOpen, "open", false, "Make an open room, which holds everyone")
	roomCreateCmd.Flags().StringSliceVar(&roomCreateMembers, "member", nil, "User or bot ID to add (repeat, or comma-separate)")
	roomCmd.AddCommand(roomCreateCmd)

	roomUpdateCmd.Flags().StringVar(&roomUpdateName, "name", "", "New room name (default: keep the current name)")
	roomUpdateCmd.Flags().StringSliceVar(&roomUpdateMembers, "member", nil, "User or bot ID who should be in the room (repeat, or comma-separate; replaces the list)")
	roomUpdateCmd.Flags().BoolVarP(&roomUpdateYes, "yes", "y", false, "Update without asking")
	roomCmd.AddCommand(roomUpdateCmd)
}
