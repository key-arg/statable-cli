package cli

import (
	"github.com/spf13/cobra"

	"github.com/key-arg/statable-cli/internal/query"
	"github.com/key-arg/statable-cli/internal/spark"
)

// fixedComplete offers a closed set of values and never falls back to file
// names, which is what a flag taking a keyword should do.
func fixedComplete(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeSite offers the site names already on disk.
//
// It never asks the server, and never touches the system keyring. A completion
// function runs on a keypress: a tab that spends a request against an hourly
// budget is bad, and one that raises a Keychain dialog or blocks on a locked
// gnome-keyring is worse. When the key lives only in the keyring this offers
// nothing, which is what the shell would have done anyway.
func completeSite(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	rt := From(cmd.Context())
	if rt == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	sites, ok := rt.loadSiteCacheCheap()
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.Name)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// registerCompletions wires the flags whose values come from a known list.
//
// Without this the generated completion scripts existed but proposed nothing,
// which is worse than no completion at all: the shell falls back to file names
// and offers the contents of the current directory as a metric.
func registerCompletions(root *cobra.Command) {
	ranges := []string{"7d", "30d", "month", "realtime", "1d", "12mo"}

	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		// A command's Flags() carries its own and every inherited persistent
		// one, but not the persistent flags it declares itself. The root
		// declares --format, so looking only at Flags() left the one flag
		// with a closed value set completing file names.
		has := func(name string) bool {
			return c.Flags().Lookup(name) != nil || c.PersistentFlags().Lookup(name) != nil
		}
		if has("format") {
			c.RegisterFlagCompletionFunc("format", fixedComplete("human", "json", "csv"))
		}
		if has("range") {
			c.RegisterFlagCompletionFunc("range", fixedComplete(ranges...))
		}
		if has("metric") {
			c.RegisterFlagCompletionFunc("metric", fixedComplete(query.AllMetrics()...))
		}
		if has("marker") {
			c.RegisterFlagCompletionFunc("marker",
				fixedComplete(string(spark.Blocks), string(spark.Braille), string(spark.ASCII)))
		}
		if has("by") {
			c.RegisterFlagCompletionFunc("by",
				fixedComplete("minute", "hour", "day", "week", "month"))
		}
		if has("compare") {
			// Only these two forms exist. Offering a third that the server
			// rejects turns completion into a source of failed commands.
			c.RegisterFlagCompletionFunc("compare",
				fixedComplete("previous_period"))
		}
		if has("dimension") {
			c.RegisterFlagCompletionFunc("dimension",
				fixedComplete(query.BreakdownDimensions()...))
		}
		if has("type") {
			c.RegisterFlagCompletionFunc("type", fixedComplete(snippetTypes...))
		}
		if has("site") {
			c.RegisterFlagCompletionFunc("site", completeSite)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
