package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/key-arg/statable-cli/internal/clierr"
)

// confirm asks before something that cannot be undone.
//
// The rule is the same one the rest of this program follows about input: a
// person at a terminal is asked, and anything else -- a pipe, CI, an agent --
// is never blocked on a question nobody can answer. Without a terminal the
// caller must have said --yes, and if they did not, the command stops with
// action_required naming the flag rather than waiting forever or, worse,
// deleting something because silence looked like consent.
//
// what is the thing being destroyed, phrased to be read back: "site
// example.com and every event recorded for it".
func (r *Runtime) confirm(yes bool, what string) error {
	if yes {
		return nil
	}
	if !r.Ctx.CanPrompt() || !term.IsTerminal(int(os.Stdin.Fd())) {
		return clierr.ActionRequired("CONFIRMATION_REQUIRED",
			fmt.Sprintf("this deletes %s, and there is no terminal to ask on", what),
			"add --yes if that is what you mean")
	}

	w := r.Out.Human()
	fmt.Fprintf(w, "This deletes %s.\nThis cannot be undone. Type yes to continue: ", what)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return clierr.ActionRequired("CONFIRMATION_REQUIRED",
			"nothing was confirmed, so nothing was deleted",
			"add --yes to skip the question")
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		// Not an error: the user answered, and the answer was no.
		return clierr.ActionRequired("CANCELLED", "cancelled, nothing was deleted")
	}
	return nil
}

// registerConfirm adds the flag every destructive command shares.
func registerConfirm(cmd *cobra.Command, yes *bool) {
	cmd.Flags().BoolVar(yes, "yes", false,
		"skip the confirmation prompt; required when there is no terminal")
}
