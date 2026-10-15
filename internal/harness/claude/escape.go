package claude

import (
	"regexp"
	"strings"
)

// Claude Code's look-alikes of <, > and /, and the invisible and combining characters it lets sit inside a tag name.
const (
	openers   = `<\x{FF1C}\x{FE64}\x{2329}\x{27E8}\x{3008}\x{2039}\x{02C2}\x{1438}\x{276C}\x{276E}\x{2770}\x{29FC}\x{226E}\x{227A}\x{22D6}`
	closers   = `>\x{FF1E}\x{FE65}\x{232A}\x{27E9}\x{3009}\x{203A}\x{02C3}\x{1433}\x{276D}\x{276F}\x{2771}\x{29FD}\x{226F}\x{227B}\x{22D7}`
	slashes   = `/\x{FF0F}\x{2215}\x{2044}`
	invisible = `\x{00AD}\x{034F}\x{0600}-\x{0605}\x{061C}\x{06DD}\x{070F}\x{0890}\x{0891}\x{08E2}\x{115F}\x{1160}\x{17B4}\x{17B5}\x{180B}-\x{180F}` +
		`\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{206F}\x{3164}\x{FE00}-\x{FE0F}\x{FEFF}\x{FFA0}\x{FFF0}-\x{FFFB}\x{110BD}\x{110CD}` +
		`\x{13430}-\x{1343F}\x{1BCA0}-\x{1BCA3}\x{1D173}-\x{1D17A}\x{16FE4}\x{E0000}-\x{E0FFF}` +
		`\x{0300}-\x{0344}\x{0346}-\x{036F}\x{0483}-\x{0489}\x{0591}-\x{05BD}\x{05BF}\x{05C1}\x{05C2}\x{05C4}\x{05C5}\x{05C7}\x{0610}-\x{061A}` +
		`\x{064B}-\x{065F}\x{0670}\x{06D6}-\x{06DC}\x{06DF}-\x{06E4}\x{06E7}\x{06E8}\x{06EA}-\x{06ED}\x{1AB0}-\x{1AFF}\x{1DC0}-\x{1DFF}` +
		`\x{20D0}-\x{20FF}\x{3099}\x{309A}\x{FE20}-\x{FE2F}\x00-\x08\x0B\x0C\x0E-\x1F\x7F-\x{9F}\x{2028}\x{2029}`
	dashes = `_\p{Pc}\x{2017}\x{02CD}\x{07FA}\x{0640}\p{Pd}\x{2212}\x{207B}\x{208B}\x{02D7}\x{2796}\x{2043}\x{30FC}\x{FF70}`
)

var (
	openerRun = regexp.MustCompile(`[` + openers + `]`)
	// closeAfter matches what follows an opener that Claude Code reads as the envelope's closing tag.
	closeAfter = regexp.MustCompile(`^(?i)[^A-Za-z0-9_\-` + openers + closers + slashes + `]*[` + slashes + `][^A-Za-z0-9_\-` + openers + closers + `]*` +
		tagName(envelope) + `(?:[^A-Za-z0-9_\-]|$)`)
)

func tagName(tag string) string {
	var b strings.Builder
	for i, r := range tag {
		if i > 0 {
			b.WriteString(`[` + invisible + `]*`)
		}
		if r == '-' || r == '_' {
			b.WriteString(`[` + dashes + `]`)
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// escapeClose writes <\ for every opener Claude Code would read as starting the envelope's closing tag, as its own
// senders do, so the body survives its exact rebuild of the envelope.
func escapeClose(text string) string {
	var b strings.Builder
	last := 0
	for _, at := range openerRun.FindAllStringIndex(text, -1) {
		rest := text[at[1]:]
		if strings.HasPrefix(rest, `\`) || !closeAfter.MatchString(rest) {
			continue
		}
		b.WriteString(text[last:at[0]])
		b.WriteString(`<\`)
		last = at[1]
	}
	b.WriteString(text[last:])
	return b.String()
}
