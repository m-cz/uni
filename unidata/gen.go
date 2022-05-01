//go:build generate
// +build generate

package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"go/format"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"zgo.at/termtext"
	"zgo.at/uni/v2/unidata"
	"zgo.at/zli"
	"zgo.at/zstd/zstring"
)

func main() {
	var err error
	if len(os.Args) > 1 {
		err = run(os.Args[1])
		zli.F(err)
		return
	}

	zli.F(run("codepoints"))
	zli.F(run("emojis"))
	zli.F(run("blocks"))
	zli.F(run("cats"))
}

func run(which string) error {
	switch which {
	case "codepoints":
		return mkcodepoints()
	case "emojis":
		return mkemojis()
	case "blocks":
		return mkblocks()
	case "cats":
		return mkcats()
	default:
		return fmt.Errorf("unknown file: %q\n", which)
	}
}

func write(fp io.Writer, s string, args ...interface{}) {
	_, err := fmt.Fprintf(fp, s, args...)
	zli.F(err)
}

func readCLDR() map[string][]string {
	d, err := fetch("https://raw.githubusercontent.com/unicode-org/cldr/master/common/annotations/en.xml")
	zli.F(err)

	var cldr struct {
		Annotations []struct {
			CP    string `xml:"cp,attr"`
			Type  string `xml:"type,attr"`
			Names string `xml:",innerxml"`
		} `xml:"annotations>annotation"`
	}
	zli.F(xml.Unmarshal(d, &cldr))

	out := make(map[string][]string)
	for _, a := range cldr.Annotations {
		if a.Type != "tts" {
			out[a.CP] = strings.Split(a.Names, " | ")
		}
	}
	return out
}

func mkcats() error {
	text, err := fetch("https://www.unicode.org/Public/UCD/latest/ucd/PropertyValueAliases.txt")
	zli.F(err)

	// gc ; Lu     ; Uppercase_Letter
	// gc ; L      ; Letter                                  # Ll | Lm | Lo | Lt | Lu
	// gc ; LC     ; Cased_Letter                            # Ll | Lt | Lu
	// gc ; Ll     ; Lowercase_Letter
	// gc ; M      ; Mark                 ; Combining_Mark   # Mc | Me | Mn
	type cat struct {
		long, short, name string
		incl              []string
	}
	var (
		cats    = make([]cat, 0, 64)
		mkconst = func(n string) string {
			return strings.ReplaceAll(n, "_", "")
		}
	)
	for _, line := range strings.Split(string(text), "\n") {
		if !strings.HasPrefix(line, "gc") {
			continue
		}

		line = strings.Trim(strings.TrimPrefix(line, "gc"), "; \t")
		names := strings.Split(line, ";")
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		var group []string
		if strings.Contains(names[len(names)-1], "#") {
			a, b := zstring.Split2(names[len(names)-1], "#")
			names[len(names)-1] = strings.TrimSpace(a)
			group = strings.Split(strings.TrimSpace(b), "|")
			for i := range group {
				group[i] = strings.TrimSpace(group[i])
			}
		}

		cats = append(cats, cat{
			name:  names[1],
			long:  mkconst(names[1]),
			short: mkconst(names[0]),
			incl:  group,
		})
	}

	out := new(bytes.Buffer)

	write(out, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n")

	write(out, "// Unicode general categories (long names).\nconst (\n\tCatUnknown = Category(iota)\n")
	for _, c := range cats {
		write(out, "\tCat%s // %-4s", c.long, c.short)
		if len(c.incl) > 0 {
			write(out, "(%s)", strings.Join(c.incl, " | "))
		}
		write(out, "\n")
	}
	write(out, ")\n")

	write(out, "// Unicode general categories (short names).\nconst (\n")
	for _, a := range cats {
		write(out, "\tCat%s = Cat%s\n", a.short, a.long)
	}
	write(out, ")\n\n")

	write(out, `
		// Categories is a list of all categories.
		var Categories = map[Category]struct {
			ShortName, Name string
			Include         []Category
		}{
	`)

	for _, c := range cats {
		write(out, "Cat%s: {", c.long)

		write(out, `%q,`, c.short)
		write(out, `%q,`, c.name)

		if len(c.incl) == 0 {
			write(out, "nil,")
		} else {
			for i := range c.incl {
				c.incl[i] = "Cat" + strings.TrimSpace(c.incl[i])
			}
			write(out, "[]Category{%s},", strings.Join(c.incl, ", "))
		}
		write(out, "},\n")
	}

	write(out, "}\n")

	f, err := format.Source(out.Bytes())
	if err != nil {
		fmt.Print(out.String())
		zli.F(err)
	}
	fp, err := os.Create("gen_cats.go")
	zli.F(err)
	defer func() { zli.F(fp.Close()) }()
	_, err = fp.Write(f)
	zli.F(err)

	return nil
}

func mkblocks() error {
	text, err := fetch("https://www.unicode.org/Public/UCD/latest/ucd/Blocks.txt")
	zli.F(err)

	var (
		consts = make([]string, 0, 256)
		ranges = new(strings.Builder)
		mkname = strings.NewReplacer(" ", "", "-", "")
	)
	for i, line := range strings.Split(string(text), "\n") {
		if c := strings.Index(line, "#"); c > -1 {
			line = strings.TrimSpace(line[:c])
		}
		if line == "" {
			continue
		}

		rng, name := zstring.Split2(line, ";")
		start, end := zstring.Split2(rng, "..")
		name = strings.TrimSpace(name)
		if name == "" || start == "" || end == "" {
			zli.Fatalf("invalid line %d in Blocks.txt: %q", i+1, line)
		}

		startI, err := strconv.ParseInt(start, 16, 64)
		zli.F(err)

		endI, err := strconv.ParseInt(end, 16, 64)
		zli.F(err)

		cname := "Block" + mkname.Replace(name)
		consts = append(consts, cname)
		write(ranges, "\t%s: {[2]rune{0x%06X, 0x%06X}, %q},\n", cname, startI, endI, name)
	}

	out := new(bytes.Buffer)
	write(out, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n")

	write(out, "// Unicode blocks\nconst (\n")
	write(out, "\tBlockUnknown = Block(iota)\n")
	for _, c := range consts {
		write(out, "\t%s\n", c)
	}
	write(out, ")\n\n")
	write(out, `
	// Blocks is a list of all Unicode blocks.
	var Blocks = map[Block]struct {
			Range [2]rune
			Name  string
		}{
	`)
	write(out, ranges.String())
	write(out, "}")

	f, err := format.Source(out.Bytes())
	if err != nil {
		fmt.Print(out.String())
		zli.F(err)
	}

	fp, err := os.Create("gen_blocks.go")
	zli.F(err)
	defer func() { zli.F(fp.Close()) }()

	_, err = fp.Write(f)
	zli.F(err)
	return nil
}

// Copy from unidata; need access to unexported stuff
type Emoji struct {
	Codepoints []rune
	Name       string
	Group      unidata.EmojiGroup
	Subgroup   unidata.EmojiSubgroup
	CLDR       []string
	SkinTones  bool
	Genders    int
}

// Copy from unidata; need access to unexported stuff
func (e Emoji) String() string {
	var c string

	// Flags
	// 1F1FF 1F1FC                                 # 🇿🇼 E2.0 flag: Zimbabwe
	// 1F3F4 E0067 E0062 E0065 E006E E0067 E007F   # 🏴󠁧󠁢󠁥󠁮󠁧󠁿 E5.0 flag: England
	if (e.Codepoints[0] >= 0x1f1e6 && e.Codepoints[0] <= 0x1f1ff) ||
		(len(e.Codepoints) > 1 && e.Codepoints[1] == 0xe0067) {
		for _, cp := range e.Codepoints {
			c += string(rune(cp))
		}
		return c
	}

	for i, cp := range e.Codepoints {
		c += string(rune(cp))

		// Don't add ZWJ as last item.
		if i == len(e.Codepoints)-1 {
			continue
		}

		switch e.Codepoints[i+1] {
		// Never add ZWJ before variation selector or skin tone.
		case 0xfe0f, 0x1f3fb, 0x1f3fc, 0x1f3fd, 0x1f3fe, 0x1f3ff:
			continue
		// Keycap: join with 0xfe0f
		case 0x20e3:
			continue
		}

		c += "\u200d"
	}
	return c
}

func mkemojis() error {
	text, err := fetch("https://unicode.org/Public/emoji/14.0/emoji-test.txt")
	zli.F(err)

	cldr := readCLDR()

	fp, err := os.Create("gen_emojis.go")
	zli.F(err)
	defer func() { zli.F(fp.Close()) }()

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n")

	var (
		emojis          = make(map[string][]string)
		order           []string
		group, subgroup string
		groups          []string
		subgroups       = make(map[string][]string)
		subgroups2      []string
	)
	for _, line := range strings.Split(string(text), "\n") {
		// Groups are listed as a comment, but we want to preserve them.
		// # group: Smileys & Emotion
		// # subgroup: face-smiling
		if strings.HasPrefix(line, "# group: ") {
			group = line[strings.Index(line, ":")+2:]
			groups = append(groups, group)
			continue
		}
		if strings.HasPrefix(line, "# subgroup: ") {
			subgroup = line[strings.Index(line, ":")+2:]
			subgroups[group] = append(subgroups[group], subgroup)
			subgroups2 = append(subgroups2, subgroup)
			continue
		}

		var comment string
		if p := strings.Index(line, "#"); p > -1 {
			comment = strings.TrimSpace(line[p+1:])
			line = strings.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		// "only fully-qualified emoji zwj sequences should be generated by
		// keyboards and other user input devices"
		if !strings.HasSuffix(line, "; fully-qualified") {
			continue
		}

		codepoints := strings.TrimSpace(strings.Split(line, ";")[0])

		// Get the name from the comment:
		//   # 😀 E2.0 grinning face
		//   # 🦶🏿 E11.0 foot: dark skin tone
		name := strings.SplitN(comment, " ", 3)[2]

		const (
			GenderNone = 0
			GenderSign = 1
			GenderRole = 2
		)

		tone := false
		gender := GenderNone
		var cp []string
		splitCodepoints := strings.Split(codepoints, " ")
		for i, c := range splitCodepoints {
			d, err := strconv.ParseInt(string(c), 16, 64)
			if err != nil {
				return err
			}

			switch d {
			// Skin tones
			case 0x1f3fb, 0x1f3fc, 0x1f3fd, 0x1f3fe, 0x1f3ff:
				tone = true
			// ZWJ
			case 0x200d:
				// No nothing

			// Old/classic gendered emoji. A "person" emoji is combined with "female
			// sign" or "male sign" to make an explicitly gendered one:
			//
			//   1F937                 # 🤷 E4.0 person shrugging
			//   1F937 200D 2642 FE0F  # 🤷‍♂️ E4.0 man shrugging
			//   1F937 200D 2640 FE0F  # 🤷‍♀️ E4.0 woman shrugging
			//
			//   2640                  # ♀ E4.0 female sign
			//   2642                  # ♂ E4.0 male sign
			//
			// Detect: 2640 or 2642 occurs in sequence position>0 to exclude just
			// the female/male signs.
			case 0x2640, 0x2642:
				if i == 0 {
					cp = append(cp, fmt.Sprintf("0x%x", d))
				} else {
					gender = GenderSign
				}
			default:
				cp = append(cp, fmt.Sprintf("0x%x", d))
			}
		}

		// This ignores combining the "holding hands", "handshake", and
		// "kissing" with different skin tone variants, where you can select a
		// different tone for each side (i.e. hand or person):
		//
		//   1F468 1F3FB 200D 1F91D 200D 1F468 1F3FF 👨🏻‍🤝‍👨🏿
		//   E12.1 men holding hands: light skin tone, dark skin tone
		//
		//   1F9D1 1F3FB 200D 2764 FE0F 200D 1F48B 200D 1F9D1 1F3FF 🧑🏻‍❤️‍💋‍🧑🏿
		//   E13.1 kiss: person, person, light skin tone, dark skin tone
		//
		// There is no good way to select this with the current UX/flagset; and
		// to be honest I don't think it's very important either, so just skip
		// it for now.
		//
		// TODO: I guess the best way to fix this is to allow multiple values
		// for -t and -g:
		//
		//   uni e handshake -t dark            Both hands dark
		//   uni e handshake -t dark -t light   Left hand dark, right hand light
		//
		// Actually, I'd change it and make multiple -t and -g flags print
		// multiple variants (like "-t light,dark" does now), and then change
		// the meaning of "-t light,dark" to the above to select multiple
		// variants in the same emoji. That makes more sense, but is not a
		// backwards-compatible change. Guess we can do it for uni 3.0.
		if tone && (strings.Contains(name, "holding hands") || strings.Contains(name, "handshake")) {
			gender = 0
			tone = false
			continue
		}
		if tone && (strings.Contains(name, "kiss:") || strings.Contains(name, "couple with heart")) {
			tone = false
			continue
		}

		key := strings.Join(cp, ", ")

		// Newer gendered emoji; combine "person", "man", or "women" with
		// something related to that:
		//
		//   1F9D1 200D 2695 FE0F # 🧑‍⚕️ E12.1 health worker
		//   1F468 200D 2695 FE0F # 👨‍⚕️ E4.0 man health worker
		//   1F469 200D 2695 FE0F # 👩‍⚕️ E4.0 woman health worker
		//
		//   1F9D1                # 🧑 E5.0 person
		//   1F468                # 👨 E2.0 man
		//   1F469                # 👩 E2.0 woman
		//
		// Detect: These only appear in the person-role and person-activity
		// subgroups; the special cases only in family subgroup.
		for _, g := range gendered {
			if strings.HasPrefix(key, g) {
				gender = GenderRole
			}
		}

		if gender == GenderRole {
			key = strings.Join(append([]string{"0x1f9d1"}, cp[1:]...), ", ")
			_, ok := emojis[key]
			if !ok {
				return fmt.Errorf("not found: %q %q", key, name)
			}

			emojis[key][5] = fmt.Sprintf("%d", gender)
			continue
		}

		if gender == GenderSign {
			_, ok := emojis[key]
			if !ok && cp[len(cp)-1] == "0xfe0f" {
				key = strings.Join(cp[0:len(cp)-1], ", ")
			}
			_, ok = emojis[key]
			if !ok {
				return fmt.Errorf("not found: %q %q", key, name)
			}

			emojis[key][5] = fmt.Sprintf("%d", gender)
			continue
		}

		if tone {
			_, ok := emojis[key]
			if !ok && cp[len(cp)-1] == "0xfe0f" {
				key = strings.Join(cp[0:len(cp)-1], ", ")
			} else if !ok {
				key = strings.Join(append(cp, "0xfe0f"), ", ")
			}
			_, ok = emojis[key]
			if !ok {
				return fmt.Errorf("not found: %q %q", key, name)
			}

			emojis[key][4] = "true"
			continue
		}

		emojis[key] = []string{
			strings.Join(cp, ", "), name, group, subgroup, "false", "0"}
		order = append(order, key)
	}

	// We should really parse it like this in the above loop, but I don't feel
	// like rewriting all of this, and this makes adding cldr easier.
	emo := make([]Emoji, len(order))
	for i, k := range order {
		e := emojis[k]

		g, _ := strconv.Atoi(e[5])
		var cp []rune
		for _, c := range strings.Split(e[0], ", ") {
			n, err := strconv.ParseUint(c[2:], 16, 32)
			zli.F(err)
			cp = append(cp, rune(n))
		}

		var groupID, subgroupID int
		for i, g := range groups {
			if g == e[2] {
				groupID = i
				break
			}
		}
		for i, g := range subgroups2 {
			if g == e[3] {
				subgroupID = i
				break
			}
		}

		emo[i] = Emoji{
			Codepoints: cp,
			Name:       e[1],
			Group:      unidata.EmojiGroup(groupID),
			Subgroup:   unidata.EmojiSubgroup(subgroupID),
			SkinTones:  e[4] == "true",
			Genders:    g,
		}
		emo[i].CLDR = cldr[strings.ReplaceAll(strings.ReplaceAll(emo[i].String(), "\ufe0f", ""), "\ufe0e", "")]
	}

	mkconst := func(n string) string {
		dash := zstring.IndexAll(n, "-")
		for i := len(dash) - 1; i >= 0; i-- {
			d := dash[i]
			n = n[:d] + string(n[d+1]^0x20) + n[d+2:]
		}
		return "Emoji" + zstring.UpperFirst(strings.ReplaceAll(n, " & ", "And"))
	}

	write(fp, "// Emoji groups.\nconst (\n")
	write(fp, "\t%s = EmojiGroup(iota)\n", mkconst(groups[0]))
	for _, g := range groups[1:] {
		write(fp, "\t%s\n", mkconst(g))
	}
	write(fp, ")\n\n")

	write(fp, `// EmojiGroups is a list of all emoji groups.
var EmojiGroups = map[EmojiGroup]struct{
	Name      string
	Subgroups []EmojiSubgroup
}{
`)
	for _, g := range groups {
		var sg []string
		for _, s := range subgroups[g] {
			sg = append(sg, mkconst(s))
		}
		write(fp, "\t%s: {%q, []EmojiSubgroup{\n\t\t%s}},\n", mkconst(g), g,
			termtext.WordWrap(strings.Join(sg, ", "), 100, "\t\t"))
	}
	write(fp, "}\n\n")

	write(fp, "// Emoji subgroups.\nconst (\n")
	first := true
	for _, g := range groups {
		for _, sg := range subgroups[g] {
			if first {
				write(fp, "\t%s = EmojiSubgroup(iota)\n", mkconst(sg))
				first = false
			} else {
				write(fp, "\t%s\n", mkconst(sg))
			}
		}
	}
	write(fp, ")\n\n")

	write(fp, `// EmojiSubgroups is a list of all emoji subgroups.
var EmojiSubgroups = map[EmojiSubgroup]struct{
	Group EmojiGroup
	Name  string
}{
`)
	for _, g := range groups {
		for _, sg := range subgroups[g] {
			write(fp, "\t%s: {%s, %q},", mkconst(sg), mkconst(g), sg)
			write(fp, "\n")
		}
	}
	write(fp, "}\n\n")

	write(fp, "var Emojis = []Emoji{\n")
	for _, e := range emo {
		var cp string
		for _, c := range e.Codepoints {
			cp += fmt.Sprintf("0x%x, ", c)
		}
		cp = cp[:len(cp)-2]

		//                   CP   Name Grp Sgr CLDR sk  gnd
		write(fp, "\t{[]rune{%s}, %q,  %d, %d, %#v, %t, %d},\n",
			cp, e.Name, e.Group, e.Subgroup, e.CLDR, e.SkinTones, e.Genders)
	}
	write(fp, "}\n\n")

	return nil
}

// TODO: add casefolding
// https://unicode.org/Public/13.0.0/ucd/CaseFolding.txt
// CaseFold []rune

// TODO: add properties:
// https://unicode.org/Public/13.0.0/ucd/PropList.txt
// "uni p dash" should print all dashes.
//
//
// TODO: add "confusable" information from
// https://www.unicode.org/Public/idna/13.0.0/
// and/or
// https://www.unicode.org/Public/security/13.0.0/
//
//
// TODO: add "alias" information from
// https://unicode.org/Public/13.0.0/ucd/NamesList.txt
// This is generated from other sources, but I can't really find where it gts
// that "x (modifier letter prime - 02B9)" from.
//
// 0027	APOSTROPHE
// 	= apostrophe-quote (1.0)
// 	= APL quote
// 	* neutral (vertical) glyph with mixed usage
// 	* 2019 is preferred for apostrophe
// 	* preferred characters in English for paired quotation marks are 2018 & 2019
// 	* 05F3 is preferred for geresh when writing Hebrew
// 	x (modifier letter prime - 02B9)
// 	x (modifier letter apostrophe - 02BC)
// 	x (modifier letter vertical line - 02C8)
// 	x (combining acute accent - 0301)
// 	x (hebrew punctuation geresh - 05F3)
// 	x (prime - 2032)
// 	x (latin small letter saltillo - A78C)

// http://www.unicode.org/reports/tr44/
func mkcodepoints() error {
	text, err := fetch("https://www.unicode.org/Public/UCD/latest/ucd/UnicodeData.txt")
	zli.F(err)

	var (
		widths   = loadwidths()
		entities = loadentities()
		digraphs = loaddigraphs()
		keysyms  = loadkeysyms()
	)

	type mapCp struct {
		cp rune
		v  string
	}

	var (
		ranges      = make([][2]rune, 0, 16)
		rangeNames  = make([]string, 0, 16)
		codepoints  = make([]string, 0, 32768)
		mapEnts     = make([]mapCp, 0, 128)
		mapDigraphs = make([]mapCp, 0, 128)
		mapKeysyms  = make([]mapCp, 0, 128)
	)
	for _, line := range bytes.Split(text, []byte("\n")) {
		if p := bytes.Index(line, []byte("#")); p > -1 {
			line = bytes.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		s := bytes.Split(line, []byte(";"))
		c, err := strconv.ParseUint(string(s[0]), 16, 32)
		zli.F(err)
		cp := rune(c)

		name := s[1]
		if name[0] == '<' {
			switch {
			// Some properties (most notably control characters) all have the name
			// as <control>, which isn't very useful. The old (obsolete) Unicode 1
			// name field has a more useful name.
			// TODO: add this information from:
			// https://www.unicode.org/Public/UCD/latest/ucd/NamesList.txt
			case len(s[10]) > 1:
				name = s[10]

			// Ranges
			// 0x3400: {0x3400, 5, 6, "<CJK Ideograph Extension A, First>", "", "", ""},
			// 0x4dbf: {0x4dbf, 5, 6, "<CJK Ideograph Extension A, Last>", "", "", ""},
			//
			//     4E00;<CJK Ideograph, First>;Lo;0;L;;;;;N;;;;;
			//     9FFF;<CJK Ideograph, Last>;Lo;0;L;;;;;N;;;;;
			//     E000;<Private Use, First>;Co;0;L;;;;;N;;;;;
			//     F8FF;<Private Use, Last>;Co;0;L;;;;;N;;;;;
			case bytes.HasSuffix(name, []byte(", First>")):
				ranges = append(ranges, [2]rune{cp, 0})
				rangeNames = append(rangeNames, strings.ReplaceAll(string(name), ", First", ""))
			case bytes.HasSuffix(name, []byte(", Last>")):
				ranges[len(ranges)-1][1] = cp
			}
		}

		if e, ok := entities[cp]; ok {
			mapEnts = append(mapEnts, mapCp{cp, e})
		}
		if d, ok := digraphs[cp]; ok {
			mapDigraphs = append(mapDigraphs, mapCp{cp, d})
		}
		if _, ok := keysyms[cp]; ok {
			mapKeysyms = append(mapKeysyms, mapCp{cp, keysyms[cp][0]})
		}

		codepoints = append(codepoints, fmt.Sprintf(
			//        CP       Wid Cat Name
			"\t0x%x: {0x%[1]x, %d, %d, %#v},\n",
			cp, widths[cp], catmap[string(s[2])], string(name)))
	}

	fp, err := os.Create("gen_codepoints.go")
	zli.F(err)
	defer func() { zli.F(fp.Close()) }()

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n")

	write(fp, `// Codepoints that aren't listed individually.
var codepointRanges = []struct {
	rng  [2]rune
	name string
}{
`)
	for i, r := range ranges {
		write(fp, "\t{[2]rune{0x%06X, 0x%06X}, %q},\n", r[0], r[1], rangeNames[i])
	}

	write(fp, "}\n\n")

	write(fp, "var Codepoints = map[rune]Codepoint{\n")
	for _, c := range codepoints {
		write(fp, "%s", c)
	}
	write(fp, "}\n\n")

	write(fp, "var htmlEntities = map[rune]string{\n")
	for _, m := range mapEnts {
		write(fp, "\t0x%02x: %q,\n", m.cp, m.v)
	}
	write(fp, "}\n\n")

	write(fp, "var keysyms = map[rune]string{\n")
	for _, m := range mapKeysyms {
		write(fp, "\t0x%02x: %q,\n", m.cp, m.v)
	}
	write(fp, "}\n\n")

	write(fp, "var digraphs = map[rune]string{\n")
	for _, m := range mapDigraphs {
		write(fp, "\t0x%02x: %q,\n", m.cp, m.v)
	}
	write(fp, "}\n\n")

	_, _, _ = mapKeysyms, mapEnts, mapDigraphs
	return nil
}

func loadwidths() map[rune]unidata.Width {
	text, err := fetch("http://www.unicode.org/Public/UCD/latest/ucd/EastAsianWidth.txt")
	zli.F(err)

	widths := make(map[rune]unidata.Width)
	for _, line := range bytes.Split(text, []byte("\n")) {
		if p := bytes.Index(line, []byte("#")); p > -1 {
			line = bytes.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		s := bytes.Split(line, []byte(";"))
		width := getwidth(string(s[1]))

		// Single codepoint.
		if !bytes.Contains(s[0], []byte("..")) {
			cp, err := strconv.ParseUint(string(s[0]), 16, 32)
			zli.F(err)

			widths[rune(cp)] = width
			continue
		}

		rng := bytes.Split(s[0], []byte(".."))
		start, err := strconv.ParseUint(string(rng[0]), 16, 32)
		zli.F(err)

		end, err := strconv.ParseUint(string(rng[1]), 16, 32)
		zli.F(err)

		for cp := start; end >= cp; cp++ {
			widths[rune(cp)] = width
		}
	}

	return widths
}

func getwidth(w string) unidata.Width {
	switch w {
	case "A":
		return unidata.WidthAmbiguous
	case "F":
		return unidata.WidthFullWidth
	case "H":
		return unidata.WidthHalfWidth
	case "N":
		return unidata.WidthNarrow
	case "Na":
		return unidata.WidthNeutral
	case "W":
		return unidata.WidthWide
	default:
		panic("wtf") // Never happens
	}
}

func loadentities() map[rune]string {
	j, err := fetch("https://html.spec.whatwg.org/entities.json")
	zli.F(err)

	var out map[string]struct {
		Codepoints []rune `json:"codepoints"`
	}
	zli.F(json.Unmarshal(j, &out))

	sorted := []string{}
	for k, _ := range out {
		// Don't need backwards-compatible versions without closing ;
		if !strings.HasSuffix(k, ";") {
			continue
		}

		sorted = append(sorted, k)
	}

	// Sort by name first, in reverse order. This way &quot; will be prefered
	// over &QUOT. Then sort by length, so we have the shortest (&nbsp; instead
	// of &NonBreakingSpace;).
	sort.Strings(sorted)
	for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
		sorted[i], sorted[j] = sorted[j], sorted[i]
	}
	// sort.Slice(sorted, func(i, j int) bool {
	// 	return len(sorted[i]) < len(sorted[j])
	// })

	entities := make(map[rune]string)
	var seen []rune
	for _, ent := range sorted {
		cp := out[ent].Codepoints

		// TODO: some entities represent two codepoints; for example
		// &NotEqualTilde; is U+02242 (MINUS TILDE) plus U+000338 (COMBINING
		// LONG SOLIDUS OVERLAY).
		// I can't be bothered to implement this right now.
		if len(cp) != 1 {
			continue
		}

		found := false
		for _, s := range seen {
			if cp[0] == s {
				found = true
				break
			}
		}
		if found {
			continue
		}

		entities[cp[0]] = strings.Trim(ent, "&;")

		// TODO: don't need seen?
		seen = append(seen, cp[0])
	}

	return entities
}

func loadkeysyms() map[rune][]string {
	header, err := fetch("https://gitlab.freedesktop.org/xorg/proto/xorgproto/-/raw/master/include/X11/keysymdef.h")
	zli.F(err)

	ks := make(map[rune][]string)
	for _, line := range strings.Split(string(header), "\n") {
		if !strings.HasPrefix(line, "#define XK") {
			continue
		}

		sp := strings.Fields(line)
		if len(sp) < 4 {
			continue
		}
		cp, err := strconv.ParseInt(strings.TrimPrefix(sp[4], "U+"), 16, 32)
		if err != nil {
			continue
		}

		ks[rune(cp)] = append(ks[rune(cp)], strings.TrimPrefix(sp[1], "XK_"))
	}

	return ks
}

func loaddigraphs() map[rune]string {
	data, err := fetch("https://tools.ietf.org/rfc/rfc1345.txt")
	zli.F(err)

	re := regexp.MustCompile(`^ .*?   +[0-9a-f]{4}`)

	dg := make(map[rune]string)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "ISO-IR-") {
			continue
		}

		if !re.MatchString(line) {
			continue
		}

		// EG     0097    END OF GUARDED AREA (EPA)
		sp := strings.Fields(line)
		cp, err := strconv.ParseInt(strings.TrimPrefix(sp[1], "U+"), 16, 32)
		if err != nil {
			continue
		}
		dg[rune(cp)] = sp[0]
	}

	// Not in the RFC but in Vim, so add manually.
	dg[0x20ac] = "=e" // € (Euro)
	dg[0x20bd] = "=R" // ₽ (Ruble); also =P and the only one with more than one digraph :-/
	return dg
}

// Load .cache/file if it exists, or fetch from URL and store in .cache if it
// doesn't.
func fetch(url string) ([]byte, error) {
	file := "./.cache/" + path.Base(url)
	if _, err := os.Stat(file); err == nil {
		return ioutil.ReadFile(file)
	}

	err := os.MkdirAll("./.cache", 0777)
	if err != nil {
		return nil, fmt.Errorf("cannot create cache directory: %s", err)
	}

	client := http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot download %q: %s", url, err)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot read body of %q: %s", url, err)
	}

	if resp.StatusCode != http.StatusOK {
		return data, fmt.Errorf("unexpected status code %d %s for %q",
			resp.StatusCode, resp.Status, url)
	}

	err = ioutil.WriteFile(file, data, 0666)
	if err != nil {
		return nil, fmt.Errorf("could not write cache: %s", err)
	}

	return data, nil
}

var gendered = []string{
	"0x1f468, 0x2695, 0xfe0f",
	"0x1f468, 0x1f393",
	"0x1f468, 0x1f3eb",
	"0x1f468, 0x2696, 0xfe0f",
	"0x1f468, 0x1f33e",
	"0x1f468, 0x1f373",
	"0x1f468, 0x1f527",
	"0x1f468, 0x1f3ed",
	"0x1f468, 0x1f4bc",
	"0x1f468, 0x1f52c",
	"0x1f468, 0x1f4bb",
	"0x1f468, 0x1f3a4",
	"0x1f468, 0x1f3a8",
	"0x1f468, 0x2708, 0xfe0f",
	"0x1f468, 0x1f680",
	"0x1f468, 0x1f692",
	"0x1f468, 0x1f9af",
	"0x1f468, 0x1f9bc",
	"0x1f468, 0x1f9bd",
	"0x1f469, 0x2695, 0xfe0f",
	"0x1f469, 0x1f393",
	"0x1f469, 0x1f3eb",
	"0x1f469, 0x2696, 0xfe0f",
	"0x1f469, 0x1f33e",
	"0x1f469, 0x1f373",
	"0x1f469, 0x1f527",
	"0x1f469, 0x1f3ed",
	"0x1f469, 0x1f4bc",
	"0x1f469, 0x1f52c",
	"0x1f469, 0x1f4bb",
	"0x1f469, 0x1f3a4",
	"0x1f469, 0x1f3a8",
	"0x1f469, 0x2708, 0xfe0f",
	"0x1f469, 0x1f680",
	"0x1f469, 0x1f692",
	"0x1f469, 0x1f9af",
	"0x1f469, 0x1f9bc",
	"0x1f469, 0x1f9bd",
}

var catmap = map[string]unidata.Category{
	// Short-hand.
	"Lu": unidata.CatUppercaseLetter,
	"Ll": unidata.CatLowercaseLetter,
	"Lt": unidata.CatTitlecaseLetter,
	"LC": unidata.CatCasedLetter,
	"Lm": unidata.CatModifierLetter,
	"Lo": unidata.CatOtherLetter,
	"L":  unidata.CatLetter,
	"Mn": unidata.CatNonspacingMark,
	"Mc": unidata.CatSpacingMark,
	"Me": unidata.CatEnclosingMark,
	"M":  unidata.CatMark,
	"Nd": unidata.CatDecimalNumber,
	"Nl": unidata.CatLetterNumber,
	"No": unidata.CatOtherNumber,
	"N":  unidata.CatNumber,
	"Pc": unidata.CatConnectorPunctuation,
	"Pd": unidata.CatDashPunctuation,
	"Ps": unidata.CatOpenPunctuation,
	"Pe": unidata.CatClosePunctuation,
	"Pi": unidata.CatInitialPunctuation,
	"Pf": unidata.CatFinalPunctuation,
	"Po": unidata.CatOtherPunctuation,
	"P":  unidata.CatPunctuation,
	"Sm": unidata.CatMathSymbol,
	"Sc": unidata.CatCurrencySymbol,
	"Sk": unidata.CatModifierSymbol,
	"So": unidata.CatOtherSymbol,
	"S":  unidata.CatSymbol,
	"Zs": unidata.CatSpaceSeparator,
	"Zl": unidata.CatLineSeparator,
	"Zp": unidata.CatParagraphSeparator,
	"Z":  unidata.CatSeparator,
	"Cc": unidata.CatControl,
	"Cf": unidata.CatFormat,
	"Cs": unidata.CatSurrogate,
	"Co": unidata.CatPrivateUse,
	"Cn": unidata.CatUnassigned,
	"C":  unidata.CatOther,

	// Lower-case shorthand.
	"lu": unidata.CatUppercaseLetter,
	"ll": unidata.CatLowercaseLetter,
	"lt": unidata.CatTitlecaseLetter,
	"lc": unidata.CatCasedLetter,
	"lm": unidata.CatModifierLetter,
	"lo": unidata.CatOtherLetter,
	"l":  unidata.CatLetter,
	"mn": unidata.CatNonspacingMark,
	"mc": unidata.CatSpacingMark,
	"me": unidata.CatEnclosingMark,
	"m":  unidata.CatMark,
	"nd": unidata.CatDecimalNumber,
	"nl": unidata.CatLetterNumber,
	"no": unidata.CatOtherNumber,
	"n":  unidata.CatNumber,
	"pc": unidata.CatConnectorPunctuation,
	"pd": unidata.CatDashPunctuation,
	"ps": unidata.CatOpenPunctuation,
	"pe": unidata.CatClosePunctuation,
	"pi": unidata.CatInitialPunctuation,
	"pf": unidata.CatFinalPunctuation,
	"po": unidata.CatOtherPunctuation,
	"p":  unidata.CatPunctuation,
	"sm": unidata.CatMathSymbol,
	"sc": unidata.CatCurrencySymbol,
	"sk": unidata.CatModifierSymbol,
	"so": unidata.CatOtherSymbol,
	"s":  unidata.CatSymbol,
	"zs": unidata.CatSpaceSeparator,
	"zl": unidata.CatLineSeparator,
	"zp": unidata.CatParagraphSeparator,
	"z":  unidata.CatSeparator,
	"cc": unidata.CatControl,
	"cf": unidata.CatFormat,
	"cs": unidata.CatSurrogate,
	"co": unidata.CatPrivateUse,
	"cn": unidata.CatUnassigned,
	"c":  unidata.CatOther,

	// Full names, underscores.
	"uppercase_letter":      unidata.CatUppercaseLetter,
	"lowercase_letter":      unidata.CatLowercaseLetter,
	"titlecase_letter":      unidata.CatTitlecaseLetter,
	"cased_letter":          unidata.CatCasedLetter,
	"modifier_letter":       unidata.CatModifierLetter,
	"other_letter":          unidata.CatOtherLetter,
	"letter":                unidata.CatLetter,
	"nonspacing_mark":       unidata.CatNonspacingMark,
	"spacing_mark":          unidata.CatSpacingMark,
	"enclosing_mark":        unidata.CatEnclosingMark,
	"mark":                  unidata.CatMark,
	"decimal_number":        unidata.CatDecimalNumber,
	"letter_number":         unidata.CatLetterNumber,
	"other_number":          unidata.CatOtherNumber,
	"number":                unidata.CatNumber,
	"connector_punctuation": unidata.CatConnectorPunctuation,
	"dash_punctuation":      unidata.CatDashPunctuation,
	"open_punctuation":      unidata.CatOpenPunctuation,
	"close_punctuation":     unidata.CatClosePunctuation,
	"initial_punctuation":   unidata.CatInitialPunctuation,
	"final_punctuation":     unidata.CatFinalPunctuation,
	"other_punctuation":     unidata.CatOtherPunctuation,
	"punctuation":           unidata.CatPunctuation,
	"math_symbol":           unidata.CatMathSymbol,
	"currency_symbol":       unidata.CatCurrencySymbol,
	"modifier_symbol":       unidata.CatModifierSymbol,
	"other_symbol":          unidata.CatOtherSymbol,
	"symbol":                unidata.CatSymbol,
	"space_separator":       unidata.CatSpaceSeparator,
	"line_separator":        unidata.CatLineSeparator,
	"paragraph_separator":   unidata.CatParagraphSeparator,
	"separator":             unidata.CatSeparator,
	"control":               unidata.CatControl,
	"format":                unidata.CatFormat,
	"surrogate":             unidata.CatSurrogate,
	"private_use":           unidata.CatPrivateUse,
	"unassigned":            unidata.CatUnassigned,
	"other":                 unidata.CatOther,

	// Without underscore.
	"uppercaseletter":      unidata.CatUppercaseLetter,
	"lowercaseletter":      unidata.CatLowercaseLetter,
	"titlecaseletter":      unidata.CatTitlecaseLetter,
	"casedletter":          unidata.CatCasedLetter,
	"modifierletter":       unidata.CatModifierLetter,
	"otherletter":          unidata.CatOtherLetter,
	"nonspacingmark":       unidata.CatNonspacingMark,
	"spacingmark":          unidata.CatSpacingMark,
	"enclosingmark":        unidata.CatEnclosingMark,
	"decimalnumber":        unidata.CatDecimalNumber,
	"letternumber":         unidata.CatLetterNumber,
	"othernumber":          unidata.CatOtherNumber,
	"connectorpunctuation": unidata.CatConnectorPunctuation,
	"dashpunctuation":      unidata.CatDashPunctuation,
	"openpunctuation":      unidata.CatOpenPunctuation,
	"closepunctuation":     unidata.CatClosePunctuation,
	"initialpunctuation":   unidata.CatInitialPunctuation,
	"finalpunctuation":     unidata.CatFinalPunctuation,
	"otherpunctuation":     unidata.CatOtherPunctuation,
	"mathsymbol":           unidata.CatMathSymbol,
	"currencysymbol":       unidata.CatCurrencySymbol,
	"modifiersymbol":       unidata.CatModifierSymbol,
	"othersymbol":          unidata.CatOtherSymbol,
	"spaceseparator":       unidata.CatSpaceSeparator,
	"lineseparator":        unidata.CatLineSeparator,
	"paragraphseparator":   unidata.CatParagraphSeparator,
	"privateuse":           unidata.CatPrivateUse,
}
