// SPDX-License-Identifier: GPL-2.0-only

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// cmdRender rewrites the generated sections of the matrix document from
// the matrix JSON. A section sits between "<!-- qamatrix:NAME -->" and
// "<!-- /qamatrix:NAME -->"; text outside the markers is left alone.
func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	var o common
	o.bind(fs)
	doc := fs.String("doc", "docs/qa/atv-matrix.md", "matrix document")
	if err := fs.Parse(args); err != nil {
		return err
	}
	m, err := loadMatrix(o.matrix)
	switch {
	case err == nil:
		// Carry operator edits in the local file to the public one.
		if err := o.save(m); err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		if m, err = loadMatrix(o.public); err != nil {
			return err
		}
	default:
		return err
	}
	m = publicView(m)
	b, err := os.ReadFile(*doc)
	if err != nil {
		return err
	}
	out := string(b)
	for name, body := range map[string]string{
		"cells":    renderCells(m),
		"results":  renderResults(m),
		"human":    renderHuman(m),
		"coverage": renderCoverage(m),
		"missing":  renderMissing(m),
	} {
		if out, err = replaceSection(out, name, body); err != nil {
			return err
		}
	}
	return os.WriteFile(*doc, []byte(out), 0o644)
}

func replaceSection(doc, name, body string) (string, error) {
	begin, end := "<!-- qamatrix:"+name+" -->", "<!-- /qamatrix:"+name+" -->"
	i := strings.Index(doc, begin)
	j := strings.Index(doc, end)
	if i < 0 || j < i {
		return doc, fmt.Errorf("document has no %s ... %s section", begin, end)
	}
	return doc[:i+len(begin)] + "\n" + body + doc[j:], nil
}

// cell escapes text for a Markdown table cell.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

func shortPlayer(p string) string {
	switch p {
	case playerNative:
		return "SP-AV"
	case playerVLC:
		return "SP-VLC"
	case playerSwiftfin:
		return "SF-AV"
	}
	return p
}

func expectText(e Expect) string {
	if e.Method == "" && e.Audio == "" && e.Subtitle == "" {
		return "(any)"
	}
	var parts []string
	parts = append(parts, map[bool]string{true: e.Method, false: "any"}[e.Method != ""])
	if e.Audio != "" {
		parts = append(parts, "a:"+e.Audio)
	}
	if e.Subtitle != "" {
		parts = append(parts, "s:"+e.Subtitle)
	}
	return strings.Join(parts, " ")
}

func dimsText(d map[string]string) string {
	order := []string{"container", "video", "range", "res", "fps", "audio", "subs", "delivery", "ops", "player", "runtime"}
	var parts []string
	for _, k := range order {
		if v := d[k]; v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func renderCells(m *Matrix) string {
	var sb strings.Builder
	group := ""
	for _, c := range m.Cells {
		if c.Group != group {
			group = c.Group
			fmt.Fprintf(&sb, "\n#### %s\n\n", group)
			sb.WriteString("| ID | Pins | Expected: SP-AV | SP-VLC | SF-AV | Why it is here |\n|---|---|---|---|---|---|\n")
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s |\n", c.ID, mdCell(c.Title), expectText(c.Expected[playerNative]),
			expectText(c.Expected[playerVLC]), expectText(c.Expected[playerSwiftfin]), mdCell(c.Risk))
	}
	sb.WriteString("\n")
	return sb.String()
}

func decisionText(d RunDecision) string {
	if d.Error != "" {
		return "error"
	}
	s := d.Method
	if d.Audio == "transcode" {
		s += ", a:tc"
	}
	if d.Subtitle != "" {
		s += ", s:" + d.Subtitle
	}
	return s
}

func checkText(it Run) string {
	results := map[string]bool{}
	var fails []string
	for _, p := range players() {
		k, ok := it.Checks[p]
		if !ok {
			continue
		}
		results[k.Result] = true
		if k.Result == "fail" && len(k.Notes) > 0 {
			fails = append(fails, shortPlayer(p)+": "+firstNote(k.Notes))
		}
	}
	switch {
	case len(results) == 0:
		return "pending"
	case results["fail"]:
		return "**fail** " + strings.Join(dedupe(fails), "; ")
	case results["pass"]:
		return "pass"
	}
	return "skip"
}

func probeText(it Run) string {
	if len(it.Probe) == 0 {
		return "pending"
	}
	var parts []string
	for _, p := range players() {
		r, ok := it.Probe[p]
		if !ok {
			continue
		}
		s := shortPlayer(p) + " " + r.Result
		if r.TTFF > 0 {
			s += fmt.Sprintf(", TTFF %.1f s", r.TTFF)
		}
		if r.Result == "fail" && len(r.Notes) > 0 {
			s = "**" + shortPlayer(p) + " fail** " + firstNote(r.Notes)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

func firstNote(notes []string) string {
	n := notes[0]
	if len(n) > 90 {
		n = n[:90] + "…"
	}
	return n
}

func dedupe(v []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range v {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func renderResults(m *Matrix) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\nDeadfin %s (API %s), mapped %s.\n\n", m.Build, m.Version, m.Generated)
	sb.WriteString("| Cell | Source | Actual: SP-AV | SP-VLC | SF-AV | Server check | AVPlayer probe | Operator TV check |\n|---|---|---|---|---|---|---|---|\n")
	for _, c := range m.Cells {
		op := c.Operator.Status
		if c.Operator.Notes != "" {
			op += " " + c.Operator.Notes
		}
		if len(c.Runs) == 0 {
			fmt.Fprintf(&sb, "| %s | no item | | | | | | %s |\n", c.ID, mdCell(op))
			continue
		}
		for i, it := range c.Runs {
			cellOp := ""
			if i == 0 {
				cellOp = mdCell(op)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", c.ID, mdCell(shortSource(it.Source)),
				decisionText(it.Decisions[playerNative]), decisionText(it.Decisions[playerVLC]), decisionText(it.Decisions[playerSwiftfin]),
				mdCell(checkText(it)), mdCell(probeText(it)), cellOp)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

// shortSource keeps the container and the video part of a media summary.
func shortSource(s string) string {
	parts := strings.SplitN(s, "; ", 3)
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, " ")
}

func renderHuman(m *Matrix) string {
	var sb strings.Builder
	sb.WriteString("\n")
	for _, c := range m.Cells {
		if c.Human == "" {
			continue
		}
		if len(c.Runs) == 0 {
			fmt.Fprintf(&sb, "- **%s** %s: %s No library item.\n", c.ID, c.Title, c.Human)
			continue
		}
		fmt.Fprintf(&sb, "- **%s** %s: %s\n", c.ID, c.Title, c.Human)
	}
	sb.WriteString("\n")
	return sb.String()
}

func renderCoverage(m *Matrix) string {
	cov := map[string]map[string][]string{}
	for _, c := range m.Cells {
		if len(c.Runs) == 0 {
			continue
		}
		for k, v := range c.Dims {
			if cov[k] == nil {
				cov[k] = map[string][]string{}
			}
			cov[k][v] = append(cov[k][v], c.ID)
		}
	}
	var sb strings.Builder
	sb.WriteString("\n| Dimension | Value | Cells with a library item |\n|---|---|---|\n")
	for _, k := range []string{"container", "video", "res", "fps", "audio", "subs", "delivery", "ops", "player", "runtime"} {
		values := make([]string, 0, len(cov[k]))
		for v := range cov[k] {
			values = append(values, v)
		}
		sort.Strings(values)
		for _, v := range values {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", k, mdCell(v), strings.Join(cov[k][v], ", "))
		}
	}
	sb.WriteString("\n")
	return sb.String()
}

func renderMissing(m *Matrix) string {
	var sb strings.Builder
	sb.WriteString("\n")
	n := 0
	for _, c := range m.Cells {
		if len(c.Runs) > 0 {
			continue
		}
		n++
		if strings.HasPrefix(c.Missing, "ffmpeg ") {
			fmt.Fprintf(&sb, "- **%s** %s. Generate a sample, or have the operator source a real one:\n\n  ```bash\n  %s\n  ```\n", c.ID, c.Title, c.Missing)
		} else {
			fmt.Fprintf(&sb, "- **%s** %s. %s\n", c.ID, c.Title, c.Missing)
		}
	}
	if n == 0 {
		sb.WriteString("Every cell has a library item.\n")
	}
	sb.WriteString("\n")
	return sb.String()
}
