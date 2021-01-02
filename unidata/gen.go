// +build go_run_only

package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"arp242.net/uni/unidata"
	"zgo.at/zli"
)

func main() {
	var err error
	if len(os.Args) > 1 {
		err = run(os.Args[1])
		zli.F(err)
		return
	}

	zli.F(run("codepoints"))
	zli.F(run("entities"))
	zli.F(run("emojis"))
}

func run(which string) error {
	switch which {
	case "codepoints":
		return mkcodepoints()
	case "entities":
		return mkentities()
	case "emojis":
		return mkemojis()
	default:
		return fmt.Errorf("unknown file: %q\n", which)
	}
}

func write(fp io.Writer, s string, args ...interface{}) {
	_, err := fmt.Fprintf(fp, s, args...)
	zli.F(err)
}

func close(fp io.Closer) { zli.F(fp.Close()) }

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

func mkemojis() error {
	text, err := fetch("https://unicode.org/Public/emoji/latest/emoji-test.txt")
	zli.F(err)

	cldr := readCLDR()

	fp, err := os.Create("emojis.go")
	zli.F(err)
	defer close(fp)

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n")

	var (
		emojis          = make(map[string][]string)
		order           []string
		group, subgroup string
		groups          []string
		subgroups       = make(map[string][]string)
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

		// This ignores combining the "holding hands" and "kissing" with
		// different skin tone variants:
		//
		// 1F468 1F3FB 200D 1F91D 200D 1F468 1F3FF 👨🏻‍🤝‍👨🏿
		// E12.1 men holding hands: light skin tone, dark skin tone
		//
		// 1F9D1 1F3FB 200D 2764 FE0F 200D 1F48B 200D 1F9D1 1F3FF 🧑🏻‍❤️‍💋‍🧑🏿
		// E13.1 kiss: person, person, light skin tone, dark skin tone
		//
		// There is no good way to select this with the current UX/flagset, and
		// to be honest I don't think it's very important either.
		if tone && strings.Contains(name, "holding hands") {
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
	emo := make([]unidata.Emoji, len(order))
	for i, k := range order {
		e := emojis[k]

		g, _ := strconv.Atoi(e[5])
		var cp []uint32
		for _, c := range strings.Split(e[0], ", ") {
			n, err := strconv.ParseUint(c[2:], 16, 32)
			zli.F(err)
			cp = append(cp, uint32(n))
		}

		emo[i] = unidata.Emoji{
			Codepoints: cp,
			Name:       e[1],
			Group:      e[2],
			Subgroup:   e[3],
			SkinTones:  e[4] == "true",
			Genders:    g,
		}
		emo[i].CLDR = cldr[strings.ReplaceAll(strings.ReplaceAll(emo[i].String(), "\ufe0f", ""), "\ufe0e", "")]
	}

	write(fp, "var Emojis = []Emoji{\n")
	for _, e := range emo {
		write(fp, "\t%s,\n", fmt.Sprintf("%#v", e)[13:])
	}
	write(fp, "}\n\n")
	write(fp, "var EmojiGroups = %#v\n\n", groups)
	write(fp, "var EmojiSubgroups = %#v\n\n", subgroups)

	return nil
}

// http://www.unicode.org/reports/tr44/
func mkcodepoints() error {
	text, err := fetch("https://www.unicode.org/Public/UCD/latest/ucd/UnicodeData.txt")
	zli.F(err)

	widths, err := loadwidths()
	zli.F(err)

	fp, err := os.Create("codepoints.go")
	zli.F(err)
	defer close(fp)

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n"+
		"var Codepoints = map[string]Codepoint{\n")

	for _, line := range bytes.Split(text, []byte("\n")) {
		if p := bytes.Index(line, []byte("#")); p > -1 {
			line = bytes.TrimSpace(line[:p])
		}
		if len(line) == 0 {
			continue
		}

		s := bytes.Split(line, []byte(";"))
		// Some properties (most notably control characters) all have the name
		// as <control>, which isn't very useful. The old (obsolete) Unicode 1
		// name field has a more useful name.
		// TODO: add this information from:
		// https://www.unicode.org/Public/UCD/latest/ucd/NamesList.txt
		name := s[1]
		if name[0] == '<' && len(s[10]) > 1 {
			name = s[10]
		}

		cp, err := strconv.ParseUint(string(s[0]), 16, 32)
		if err != nil {
			return err
		}

		write(fp, "\t\"%s\": {%d, %d, 0x%x, \"%s\"},\n",
			s[0], widths[cp], unidata.Catmap[string(s[2])], cp, name)
	}

	write(fp, "}\n")
	return nil
}

func loadwidths() (map[uint64]uint8, error) {
	text, err := fetch("http://www.unicode.org/Public/UCD/latest/ucd/EastAsianWidth.txt")
	if err != nil {
		return nil, err
	}

	widths := make(map[uint64]uint8)
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
			if err != nil {
				return nil, err
			}
			widths[cp] = width
			continue
		}

		rng := bytes.Split(s[0], []byte(".."))
		start, err := strconv.ParseUint(string(rng[0]), 16, 32)
		if err != nil {
			return nil, err
		}
		end, err := strconv.ParseUint(string(rng[1]), 16, 32)
		if err != nil {
			return nil, err
		}

		for cp := start; end >= cp; cp++ {
			widths[cp] = width
		}
	}

	return widths, nil
}

func getwidth(w string) uint8 {
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

func mkentities() error {
	j, err := fetch("https://html.spec.whatwg.org/entities.json")
	if err != nil {
		return err
	}

	var out map[string]struct {
		Codepoints []uint32 `json:"codepoints"`
	}
	err = json.Unmarshal(j, &out)
	if err != nil {
		return err
	}

	fp, err := os.Create("entities.go")
	if err != nil {
		return err
	}
	defer close(fp)

	write(fp, "// Code generated by gen.go; DO NOT EDIT\n\n"+
		"package unidata\n\n"+
		"var Entities = map[rune]string{\n")

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
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) < len(sorted[j])
	})

	var seen []uint32
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

		write(fp, "\t%d: \"%s\",\n", cp[0], strings.Trim(ent, "&;"))

		seen = append(seen, cp[0])
	}

	write(fp, "}\n")
	return nil
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
