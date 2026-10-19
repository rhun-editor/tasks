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
)

const maxDiscoveredTasks = 872
const maxTaskSourceBytes = 1 << 20
const maxTaskSourceLine = 64 << 10

var targetLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_./-]*)\s*:([^=]|$)`)
var recipeLine = regexp.MustCompile(`^@?([A-Za-z_][A-Za-z0-9_-]*)(\s+[^:]*)?:(\s|$)`)

type taskScan struct {
	tasks   []task
	warning string
}

func (s *taskScan) warn(message string) {
	if s.warning == "" {
		s.warning = message
	}
}

// scan preserves discovery's public test surface; rescan also consumes its bounded warning.
func scan(project string) []task { return discoverTasks(project).tasks }
func discoverTasks(project string) taskScan {
	var result taskScan
	seen := map[string]bool{}
	add := func(kind, what, title, command string) {
		if len(result.tasks) == maxDiscoveredTasks {
			result.warn("More than 872 discovered tasks; the first 872 remain available")
			return
		}
		if err := (managedSpec{Label: title, Command: command, Workspace: "."}).Validate(); err != nil {
			result.warn("Some discovered tasks have unsupported or oversized names")
			return
		}
		base := kind + "_" + slug(what)
		name := base
		for n := 2; seen[name]; n++ {
			suffix := "_" + strconv.Itoa(n)
			prefix := base
			if len(prefix) > 40-len(suffix) {
				prefix = prefix[:40-len(suffix)]
			}
			name = prefix + suffix
		}
		seen[name] = true
		result.tasks = append(result.tasks, task{name: name, title: title, command: command})
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
			add("make", m[1], "make "+m[1], "make "+taskShellWord(m[1]))
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
				add("just", m[1], "just "+m[1], "just "+taskShellWord(m[1]))
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
			runner := "npm run"
			if taskSourceExists(project, "pnpm-lock.yaml") {
				runner = "pnpm run"
			} else if taskSourceExists(project, "yarn.lock") {
				runner = "yarn run"
			} else if taskSourceExists(project, "bun.lockb") || taskSourceExists(project, "bun.lock") {
				runner = "bun run"
			}
			names := make([]string, 0, len(pkg.Scripts))
			for key := range pkg.Scripts {
				names = append(names, key)
			}
			sort.Strings(names)
			for _, name := range names {
				separator := " "
				if strings.HasPrefix(name, "-") {
					separator = " -- "
				}
				add("npm", name, "npm run "+name, runner+separator+taskShellWord(name))
			}
		}
	}
	if data, ok := read("Cargo.toml"); ok && data != nil {
		for _, name := range []string{"build", "test", "run", "check", "clippy"} {
			add("cargo", name, "cargo "+name, "cargo "+name)
		}
	}
	if data, ok := read("go.mod"); ok && data != nil {
		for _, name := range []string{"build ./...", "test ./...", "vet ./...", "run ."} {
			add("go", strings.Fields(name)[0], "go "+name, "go "+name)
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
