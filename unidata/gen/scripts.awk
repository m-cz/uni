BEGIN        { FS = " *[;#] *"
               PROCINFO["sorted_in"] = "@ind_str_asc" }
/^$/ || /^#/ { next }

{
    split($1, se, /\.\./)
    start = strtonum("0x" se[1])
    end   = se[2] == "" ? start : strtonum("0x" se[2])
    name  = $2

    scripts[name] = sprintf("{0x%04X, 0x%04X},\n%s", start, end, scripts[name])
}

END {
    print("// Code generated by gen.zsh; DO NOT EDIT\n\npackage unidata\n")

    print("// Unicode scripts\nconst (\n" \
          "\tScriptUnknown = Script(iota)")
    for (k in scripts)
        print("\t" mkconst(k))
    print(")\n")

    print("// Scripts is a list of all Unicode scripts.\n" \
          "var Scripts = map[Script]struct {\n" \
              "\tName   string\n" \
              "\tRanges [][2]rune\n" \
          "}{\n" \
          "\tScriptUnknown: {\"Unknown\", nil},")
    for (k in scripts)
        printf("\t%s: {\"%s\", [][2]rune{\n%s}},\n", mkconst(k), gensub(/_/, " ", "g", k), scripts[k])
    print("}")
}

function mkconst(s,     i) {
    while (i = index(s, "_"))
        s = substr(s, 0, i-1) toupper(substr(s, i+1, 1)) substr(s, i+2)
    return "Script" toupper(substr(s, 1, 1)) substr(gensub(/ & /, "And", "g", s), 2)
}