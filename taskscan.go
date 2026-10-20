package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

const maxDiscoveredTasks = 872
const maxTaskSourceBytes = 1 << 20
const maxTaskSourceLine = 64 << 10

var targetLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_./-]*)\s*:([^=]|$)`)
var recipeLine = regexp.MustCompile(`^@?([A-Za-z_][A-Za-z0-9_-]*)(\s+[^:]*)?:(\s|$)`)

// a dev server's ready line: the URL it serves on
var serverReady = []string{"{_}http://localhost{_}", "{_}http://127.0.0.1{_}"}

// entry is a task and the name part of its palette command (make_build)
type entry struct {
	name string
	task rhunext.Task
}

type taskScan struct {
	tasks   []entry
	warning string
}

func (s *taskScan) warn(message string) {
	if s.warning == "" {
		s.warning = message
	}
}

// program runs a command: argv form on Windows (npm.cmd, go, cargo, make), a shell line elsewhere
func program(windows bool, args ...string) rhunext.Program {
	if windows {
		return rhunext.Program{Argv: args}
	}
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = taskShellWord(a)
	}
	return rhunext.Program{Shell: strings.Join(words, " ")}
}

// scan preserves discovery's public test surface; rescan also consumes its bounded warning.
func scan(project string) []entry { return discoverTasks(project, runtime.GOOS == "windows").tasks }

func discoverTasks(project string, windows bool) taskScan {
	var result taskScan
	seenName, seenID := map[string]bool{}, map[string]bool{}
	defaults := map[string]bool{}
	add := func(kind, what string, t rhunext.Task) {
		if len(result.tasks) == maxDiscoveredTasks {
			result.warn("More than 872 discovered tasks; the first 872 remain available")
			return
		}
		base := kind + "_" + slug(what)
		name := base
		for n := 2; seenName[name]; n++ {
			suffix := "_" + strconv.Itoa(n)
			prefix := base
			if len(prefix) > 40-len(suffix) {
				prefix = prefix[:40-len(suffix)]
			}
			name = prefix + suffix
		}
		id := kind + "." + strings.ReplaceAll(slug(what), "_", "-")
		for n := 2; seenID[id]; n++ {
			id = kind + "." + strings.ReplaceAll(slug(what), "_", "-") + "-" + strconv.Itoa(n)
		}
		t.ID = id
		// one default per group: the first discovered
		if t.Default {
			t.Default = !defaults[t.Group]
			defaults[t.Group] = true
		}
		if _, err := t.Encode(); err != nil {
			result.warn("Some discovered tasks have unsupported or oversized names")
			return
		}
		seenName[name], seenID[id] = true, true
		result.tasks = append(result.tasks, entry{name: name, task: t})
	}
	read := func(name string) ([]byte, bool) {
		data, err := readTaskSource(project, name, maxTaskSourceBytes)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}
		if err != nil {
			result.warn("Cannot read " + name + ": " + err.Error())
			return nil, true
		}
		return data, true
	}
	lines := func(data []byte, name string, parse func(string)) {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 4096), maxTaskSourceLine+1)
		for sc.Scan() {
			if len(result.tasks) == maxDiscoveredTasks {
				result.warn("More than 872 discovered tasks; the first 872 remain available")
				break
			}
			parse(sc.Text())
		}
		if sc.Err() != nil {
			result.warn(name + " contains a line longer than 64 KiB")
		}
	}
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		data, ok := read(name)
		if !ok {
			continue
		}
		if data == nil {
			break
		}
		targets := map[string]bool{}
		lines(data, name, func(line string) {
			m := targetLine.FindStringSubmatch(line)
			if m == nil || strings.Contains(line, "%") || targets[m[1]] {
				return
			}
			targets[m[1]] = true
			t := rhunext.Task{Label: "make " + m[1], Program: program(windows, "make", m[1]),
				Matchers: []rhunext.Matcher{{Builtin: "gcc"}}}
			switch m[1] {
			case "all", "build":
				t.Group, t.Default = "build", true
			case "test", "check":
				t.Group, t.Default = "test", true
			}
			add("make", m[1], t)
		})
		break
	}
	for _, name := range []string{"justfile", "Justfile", ".justfile"} {
		data, ok := read(name)
		if !ok {
			continue
		}
		if data == nil {
			break
		}
		lines(data, name, func(line string) {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") || strings.Contains(line, ":=") {
				return
			}
			m := recipeLine.FindStringSubmatch(line)
			if m != nil && !strings.HasPrefix(m[1], "_") {
				add("just", m[1], rhunext.Task{Label: "just " + m[1], Program: program(windows, "just", m[1])})
			}
		})
		break
	}
	if data, ok := read("package.json"); ok && data != nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			result.warn("Cannot parse package.json")
		} else {
			runner := "npm"
			if taskSourceExists(project, "pnpm-lock.yaml") {
				runner = "pnpm"
			} else if taskSourceExists(project, "yarn.lock") {
				runner = "yarn"
			} else if taskSourceExists(project, "bun.lockb") || taskSourceExists(project, "bun.lock") {
				runner = "bun"
			}
			exe := runner
			if windows && runner != "bun" {
				exe += ".cmd"
			}
			names := make([]string, 0, len(pkg.Scripts))
			for key := range pkg.Scripts {
				names = append(names, key)
			}
			sort.Strings(names)
			for _, name := range names {
				args := []string{exe, "run", name}
				if strings.HasPrefix(name, "-") {
					args = []string{exe, "run", "--", name}
				}
				t := rhunext.Task{Label: runner + " run " + name, Program: program(windows, args...)}
				switch name {
				case "build":
					t.Group, t.Default = "build", true
				case "test":
					t.Group, t.Default = "test", true
				case "dev", "start", "serve":
					t.Ready = serverReady
				}
				if strings.Contains(pkg.Scripts[name], "tsc") {
					t.Matchers = []rhunext.Matcher{{Builtin: "tsc"}}
				}
				add("npm", name, t)
			}
		}
	}
	if data, ok := read("Cargo.toml"); ok && data != nil {
		rustc := []rhunext.Matcher{{Builtin: "rustc"}}
		for _, name := range []string{"build", "test", "run", "check", "clippy"} {
			t := rhunext.Task{Label: "cargo " + name, Program: program(windows, "cargo", name)}
			switch name {
			case "build":
				t.Group, t.Default, t.Matchers = "build", true, rustc
			case "check", "clippy":
				t.Group, t.Matchers = "build", rustc
			case "test":
				t.Group, t.Default, t.Matchers = "test", true, rustc
			}
			add("cargo", name, t)
		}
	}
	if data, ok := read("go.mod"); ok && data != nil {
		gom := []rhunext.Matcher{{Builtin: "go"}}
		for _, args := range [][]string{{"build", "./..."}, {"test", "./..."}, {"vet", "./..."}, {"run", "."}} {
			t := rhunext.Task{Label: "go " + strings.Join(args, " "), Program: program(windows, append([]string{"go"}, args...)...)}
			switch args[0] {
			case "build":
				t.Group, t.Default, t.Matchers = "build", true, gom
			case "test":
				t.Group, t.Default, t.Matchers = "test", true, gom
			case "vet":
				t.Matchers = gom
			}
			add("go", args[0], t)
		}
	}
	return result
}

func taskShellWord(word string) string { return taskShellWordFor(word, runtime.GOOS == "windows") }
func taskShellWordFor(word string, windows bool) string {
	safe := word != "" && !(windows && strings.HasPrefix(word, "@"))
	for _, r := range word {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:@+-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return word
	}
	if windows {
		return "'" + strings.ReplaceAll(word, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
}

func taskWarningText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= 240 {
		return s
	}
	s = s[:240]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}

func taskRegistrationWarning(count int) string {
	return fmt.Sprintf("%d tasks available in Run Task; only 64 have individual palette commands", count)
}
