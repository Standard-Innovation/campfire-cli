package commands

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Standard-Innovation/campfire-cli/internal/errors"
)

var idPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// requireID checks that an argument is a positive numeric ID before it goes
// into a path or a request body.
func requireID(arg, noun string) (string, error) {
	arg = strings.TrimSpace(arg)
	if !idPattern.MatchString(arg) {
		return "", errors.NewInvalidArgsError(fmt.Sprintf("Invalid %s ID %q: expected a number", noun, arg))
	}
	return arg, nil
}
