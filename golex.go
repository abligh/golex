package main

import (
	"fmt"
	"flag"
	"os"
	"io"
	"bytes"
	"strings"
	"regexp"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "golex: Usage: %s [flags] file.l...\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	f, err := os.Open(flag.Arg(0))
	if f == nil {
		fmt.Fprintf(os.Stderr, "golex: could not open '%s': %v\n", flag.Arg(0), err)
		os.Exit(1)
	}

	fs, _ := f.Stat()
	data := make([]byte, fs.Size)
	f.Read(data)
	f.Close()

	out, _ := os.Create(flag.Arg(0) + ".go")
	parse(data, out)
	out.Close()
}

func readLine(in io.RuneReader) (string, bool) {
	line := ""

	for {
		rune, _, err := in.ReadRune()
		if err != nil {
			return line, true
		}
		if rune == '\n' {
			return line, false
		}
		line += string(rune)
	}

	return line, false
}

func trimComments(line string, inComment bool) (string, bool) {
	if !inComment {
		idx := strings.Index(line, "/*")
		if idx != -1 {
			trimmed, stillIn := trimComments(line[idx:], true)
			return line[:idx] + trimmed, stillIn
		}
		return line, false
	} 
		
	// In comment.
	idx := strings.Index(line, "*/")
	if idx == -1 {
		return "", true
	}

	return trimComments(line[idx + 2:], false)
}

type parseState int
const (
	PROLOGUE parseState = iota
	PROLOGUE_LIT
	ACTIONS
	ACTIONS_CONT
	EPILOGUE
)

type regexpParseState int
const (
	ROOT regexpParseState = iota
	QUOTES
	CLASS
	SUBST
)

func parse(data []byte, out io.Writer) {
	buffer := bytes.NewBuffer(data)
	state := PROLOGUE
	inComment := false
	wroteEnd := false

	patternSubstitutions := make(map[string]string)
	firstPattern := true
	lastPattern := ""

	for {
		line, eof := readLine(buffer)
		if len(line) == 0 && eof {
			break
		} else if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		if line == "%%" {
			// State transition
			switch state {
			case PROLOGUE:
				state = ACTIONS
				out.Write([]byte(`
					import (
						"regexp"
						"io"
						"bufio"
						"os"
					)

					var yyin io.Reader
					var yyout io.Writer = os.Stdout
					type yyrule struct {
						regexp *regexp.Regexp
						action func(yytext string)
					}
					var yyrules []yyrule = []yyrule{`))

			case ACTIONS:
				state = EPILOGUE
				wroteEnd = true
				out.Write([]byte("}\n"))
			}
		} else {
			switch state {
			case PROLOGUE:
				line, inComment = trimComments(line, inComment)
				if len(strings.TrimSpace(line)) == 0 { continue }

				if line == "%{" {
					state = PROLOGUE_LIT
				} else {
					if line[0] == ' ' || line[0] == '\t' {
						out.Write([]byte(strings.TrimSpace(line) + "\n"))
					} else {
						firstSpace := strings.Index(line, " ")
						firstTab := strings.Index(line, "\t")
						if firstSpace == -1 && firstTab == -1 {
							panic(fmt.Sprintf("don't know what to do with line \"%s\" in PROLOGUE", line))
						}

						smaller := firstSpace
						if smaller == -1 {
							smaller = firstTab
						}
						if firstTab != -1 && firstTab < smaller {
							smaller = firstTab
						}

						patternSubstitutions[line[:smaller]] = strings.TrimSpace(line[smaller:])
					}
				}

			case PROLOGUE_LIT:
				if line == "%}" {
					state = PROLOGUE
				} else {
					out.Write([]byte(line + "\n"))
				}

			case ACTIONS:
				// Work out what the actual pattern is.
				pi, rps := 0, ROOT
				ll := len(line)
				qStart := 0

				for ; pi < ll; pi++ {
					if line[pi] == '\\' {
						pi++
						continue
					}

					switch rps {
					case ROOT:
						switch line[pi] {
						case ' ', '\t': goto parsed
						case '[': 	rps = CLASS
						case '"':	rps = QUOTES; qStart = pi
						case '{':	rps = SUBST; qStart = pi
						}
					case CLASS:
						if line[pi] == ']' {
							rps = ROOT
						}
					case QUOTES:
						if line[pi] == '"' {
							origQuoted := line[qStart + 1:pi]
							quoted := strings.Replace(origQuoted, "\\\"", "\"", -1)
							quoted = regexp.QuoteMeta(quoted)

							line = line[:qStart] + quoted + line[pi + 1:]
							pi += len(quoted) - len(origQuoted) - 2

							rps = ROOT
						}
					case SUBST:
						if line[pi] == '}' {
							name := line[qStart + 1:pi]
							repl, found := patternSubstitutions[name]
							if !found {
								panic(fmt.Sprintf("substitution {%s} found, but no such name!", name))
							}

							line = line[:qStart] + "(" + repl + ")" + line[pi + 1:]
							pi += 2 + len(repl) - len(name) - 2

							rps = ROOT
						}
					}
				}

			parsed: 
				quotedPattern := line[:pi]
				quotedPattern = strings.Replace(quotedPattern, "\\", "\\\\", -1)
				quotedPattern = strings.Replace(quotedPattern, "\"", "\\\"", -1)
				quotedPattern = "\"" + quotedPattern + "\""

				if firstPattern {
					firstPattern = false
				} else {
					out.Write([]byte(",\n"))
				}

				out.Write([]byte("{regexp.MustCompile(" + quotedPattern + "), func(yytext string) {\n"))

				lastPattern = strings.TrimSpace(line[pi:])

				if len(lastPattern) > 0 {
					if lastPattern[0] == '{' {
						if lastPattern[len(lastPattern)-1] == '}' {
							lastPattern = lastPattern[:len(lastPattern)-1]
						} else {
							state = ACTIONS_CONT
						}

						lastPattern = lastPattern[1:]
					}
				}

				if state == ACTIONS {
					out.Write([]byte(lastPattern + "\n}}"))
				}

			case ACTIONS_CONT:
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '}' {
					lastPattern = strings.TrimSpace(lastPattern + line)
					lastPattern = lastPattern[:len(lastPattern)-1]
					out.Write([]byte(lastPattern + "\n}}"))
					state = ACTIONS
				} else {
					lastPattern += line + "\n"
				}

			case EPILOGUE:
				out.Write([]byte(line + "\n"))
			}
		}
	}

	if !wroteEnd {
		out.Write([]byte("}\n"))
	}

	out.Write([]byte(`
		func yylex() {
			reader := bufio.NewReader(yyin)

			data := ""

			for {
				line, err := reader.ReadString('\n')
				if len(line) == 0 && err == os.EOF {
					break
				}

				data += line
			}

			// Lex data.

			for len(data) > 0 {
				longestMatch, longestMatchLen := (func(string))(nil), -1
				for _, v := range yyrules {
					idxs := v.regexp.FindStringIndex(data)
					if idxs != nil && idxs[0] == 0 {
						if idxs[1] > longestMatchLen {
							longestMatch, longestMatchLen = v.action, idxs[1]
						}
					}
				}

				if longestMatch == nil {
					yyout.Write([]byte{data[0]})
					data = data[1:]
				} else {
					longestMatch(data[:longestMatchLen])
					data = data[longestMatchLen:]
				}
			}
		}` + "\n"))
}
