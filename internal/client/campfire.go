package client

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
)

// Campfire authenticates people with personal access tokens (sent as a Bearer
// header) and bots with bot keys (embedded in the URL path as "<id>-<token>").
// The two reach messages through different routes and post different bodies,
// so the CLI's commands go through these helpers rather than building paths.

var botKeyPattern = regexp.MustCompile(`^[0-9]+-[A-Za-z0-9]+$`)

// IsBotKey reports whether a credential is a Campfire bot key rather than a
// personal access token.
func IsBotKey(credential string) bool {
	return botKeyPattern.MatchString(credential)
}

var botKeySegment = regexp.MustCompile(`/[0-9]+-[A-Za-z0-9]{8,}(/|$|\?)`)

// RedactBotKeys masks bot keys embedded in URL paths so they never reach logs
// or command output.
func RedactBotKeys(s string) string {
	return botKeySegment.ReplaceAllString(s, "/[BOT_KEY]$1")
}

// Campfire wraps an API client with the credential it was built from.
type Campfire struct {
	API API
	// BotKey is set when authenticating as a bot; Token auth leaves it empty.
	BotKey string
}

// NewCampfire returns a Campfire client for the given base URL and credential.
func NewCampfire(baseURL, credential string) (*Campfire, *Client) {
	if IsBotKey(credential) {
		c := New(baseURL, "")
		return &Campfire{API: c, BotKey: credential}, c
	}
	c := New(baseURL, credential)
	return &Campfire{API: c}, c
}

// IsBot reports whether requests are made as a bot.
func (cf *Campfire) IsBot() bool {
	return cf.BotKey != ""
}

// Me returns the authenticated person's profile.
func (cf *Campfire) Me() (*APIResponse, error) {
	if cf.IsBot() {
		return nil, ErrNeedsToken("Showing your profile")
	}
	return cf.API.Get("/users/me/profile.json")
}

// Rooms lists the rooms the authenticated person belongs to.
func (cf *Campfire) Rooms() (*APIResponse, error) {
	if cf.IsBot() {
		return nil, ErrNeedsToken("Listing rooms")
	}
	return cf.API.Get("/rooms.json")
}

// MessagesPage identifies a page of a room's messages. Before and After are
// message IDs; leave both empty for the latest page.
type MessagesPage struct {
	Before string
	After  string
}

// Messages fetches one page of a room's messages, oldest first. The response's
// LinkNext points at the next page in the same direction.
func (cf *Campfire) Messages(roomID string, page MessagesPage) (*APIResponse, error) {
	query := url.Values{}
	if page.Before != "" {
		query.Set("before", page.Before)
	}
	if page.After != "" {
		query.Set("after", page.After)
	}
	path := cf.messagesPath(roomID)
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return cf.API.Get(path)
}

// Post creates a message in a room.
func (cf *Campfire) Post(roomID, body string) (*APIResponse, error) {
	if cf.IsBot() {
		return cf.API.PostText(cf.messagesPath(roomID), body)
	}
	return cf.API.Post(cf.messagesPath(roomID), map[string]any{
		"message": map[string]any{"body": body},
	})
}

func (cf *Campfire) messagesPath(roomID string) string {
	room := url.PathEscape(roomID)
	if cf.IsBot() {
		return fmt.Sprintf("/rooms/%s/%s/messages", room, url.PathEscape(cf.BotKey))
	}
	return fmt.Sprintf("/rooms/%s/messages.json", room)
}

// ErrNeedsToken is returned for operations Campfire only offers to people.
func ErrNeedsToken(action string) error {
	e := errors.NewInvalidArgsError(action + " needs a personal access token")
	e.Hint = "Bot keys can only read and post messages in rooms the bot belongs to. Create a token from your Campfire profile, then run 'campfire auth login TOKEN'"
	return e
}
