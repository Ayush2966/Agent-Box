package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/agentbox-dev/agentbox/internal/api"
)

func formatPorts(p map[string]int) string {
	if len(p) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, p[k]))
	}
	return strings.Join(parts, " ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printTable(out io.Writer, boxes []api.BoxView, showRepo bool) {
	if len(boxes) == 0 {
		fmt.Fprintln(out, "no boxes (create one with `agentbox new <name>`)")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	header := "NAME\tSTATE\tPORTS\tDB\tAHEAD\tDIRTY"
	if showRepo {
		header = "REPO\t" + header
	}
	fmt.Fprintln(tw, header)
	for _, b := range boxes {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%d", b.Name, b.State, formatPorts(b.Ports), dash(b.DBName), b.Ahead, b.Dirty)
		if showRepo {
			row = b.RepoPath + "\t" + row
		}
		fmt.Fprintln(tw, row)
	}
	tw.Flush()
}

func printBox(out io.Writer, b api.BoxView) {
	fmt.Fprintf(out, "\nbox %s is %s\n", b.Name, b.State)
	fmt.Fprintf(out, "  dir     %s\n", b.Dir)
	fmt.Fprintf(out, "  branch  %s\n", b.Branch)
	fmt.Fprintf(out, "  ports   %s\n", formatPorts(b.Ports))
	if b.DBName != "" {
		fmt.Fprintf(out, "  db      %s\n", b.DBName)
	}
	if b.LastError != "" {
		fmt.Fprintf(out, "  error   %s\n", b.LastError)
	}
	fmt.Fprintf(out, "\npoint your agent at it:\n  cd %q\n", b.Dir)
}
