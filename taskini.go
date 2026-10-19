package main

// .rhun/tasks.ini: the project's own tasks, one section per task, listed before discovered ones.
//
//	[serve]
//	label = Dev server
//	command = python3 -m http.server 8765
//	# or: argv = python3 -m http.server 8765   (split on spaces; "quote words" that hold them)
//	cwd = web
//	env.PORT = 8765
//	group = build
//	default = yes
//	ready = Serving HTTP on {_}
//	matcher = go
//	# or an inline matcher: matcher.1 = {file}:{line}: {message}, matcher.severity = warning
//	depends = build lint
//	depends.order = sequence

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

const maxTaskConfigBytes = 64 << 10
const maxConfiguredTasks = 128
const taskConfigPath = ".rhun/tasks.ini"

func readConfiguredTasks(project string) ([]entry, error) {
	data, err := readTaskSource(project, taskConfigPath, maxTaskConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeTaskINI(string(data))
}

// splitArgv splits on blanks; "double quotes" keep a word's blanks ("" is a quote inside one)
func splitArgv(s string) ([]string, error) {
	var out []string
	var word strings.Builder
	in, has := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && in && i+1 < len(s) && s[i+1] == '"':
			word.WriteByte('"')
			i++
		case c == '"':
			in, has = !in, true
		case (c == ' ' || c == '\t') && !in:
			if has || word.Len() > 0 {
				out = append(out, word.String())
				word.Reset()
				has = false
			}
		default:
			word.WriteByte(c)
		}
	}
	if in {
		return nil, errors.New("argv: a quote is not closed")
	}
	if has || word.Len() > 0 {
		out = append(out, word.String())
	}
	return out, nil
}

type iniTask struct {
	id    string
	keys  []string
	vals  []string
	line0 int
}

func decodeTaskINI(text string) ([]entry, error) {
	if len(text) > maxTaskConfigBytes {
		return nil, fmt.Errorf("more than %d bytes", maxTaskConfigBytes)
	}
	var sections []*iniTask
	var cur *iniTask
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("line %d: a section is [ID]", n+1)
			}
			cur = &iniTask{id: strings.TrimSpace(line[1 : len(line)-1]), line0: n + 1}
			if !rhunext.TaskIDOK(cur.id) {
				return nil, fmt.Errorf("line %d: [%s]: a task id is 1 to 64 of a-z 0-9 _ . -, from a letter or digit", n+1, cur.id)
			}
			for _, s := range sections {
				if s.id == cur.id {
					return nil, fmt.Errorf("line %d: [%s] twice", n+1, cur.id)
				}
			}
			if len(sections) == maxConfiguredTasks {
				return nil, fmt.Errorf("more than %d tasks", maxConfiguredTasks)
			}
			sections = append(sections, cur)
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 || cur == nil {
			return nil, fmt.Errorf("line %d: KEY = VALUE in a [task] section", n+1)
		}
		cur.keys = append(cur.keys, strings.TrimSpace(line[:eq]))
		cur.vals = append(cur.vals, strings.TrimSpace(line[eq+1:]))
	}
	out := make([]entry, 0, len(sections))
	for _, s := range sections {
		t, err := s.task()
		if err != nil {
			return nil, fmt.Errorf("[%s]: %v", s.id, err)
		}
		out = append(out, entry{name: "config_" + slug(s.id), task: t})
	}
	return out, nil
}

func (s *iniTask) task() (rhunext.Task, error) {
	t := rhunext.Task{ID: s.id, Label: s.id}
	var inline []string
	var head []string
	for i, k := range s.keys {
		v := s.vals[i]
		switch {
		case k == "label":
			t.Label = v
		case k == "detail":
			t.Detail = v
		case k == "command":
			t.Program.Shell = v
		case k == "argv":
			argv, err := splitArgv(v)
			if err != nil {
				return t, err
			}
			t.Program.Argv = argv
		case k == "cwd":
			t.Program.Cwd = v
		case strings.HasPrefix(k, "env."):
			t.Program.Env = append(t.Program.Env, rhunext.EnvVar{Name: k[4:], Value: v})
		case k == "unset":
			t.Program.Unset = append(t.Program.Unset, strings.Fields(v)...)
		case k == "group":
			t.Group = v
		case k == "default":
			t.Default = v == "yes" || v == "true"
		case k == "reveal":
			t.Reveal = rhunext.Reveal(v)
		case k == "ready":
			t.Ready = append(t.Ready, v)
		case k == "busy":
			t.Busy = append(t.Busy, v)
		case k == "depends":
			t.Depends = strings.Fields(v)
		case k == "depends.order":
			if v != "sequence" && v != "parallel" {
				return t, errors.New("depends.order is sequence or parallel")
			}
			t.Parallel = v == "parallel"
		case k == "matcher":
			for _, name := range strings.Fields(v) {
				t.Matchers = append(t.Matchers, rhunext.Matcher{Builtin: name})
			}
		case k == "matcher.severity" || k == "matcher.columns":
			head = append(head, strings.TrimPrefix(k, "matcher.")+" "+v)
		case k == "matcher.loop":
			if v == "yes" || v == "true" {
				head = append(head, "loop")
			}
		case len(k) == 9 && strings.HasPrefix(k, "matcher.") && k[8] >= '1' && k[8] <= '4':
			inline = append(inline, k[8:]+" "+v)
		default:
			return t, fmt.Errorf("unknown key %q", k)
		}
	}
	if t.Program.Shell != "" && len(t.Program.Argv) > 0 {
		return t, errors.New("command or argv, not both")
	}
	if len(inline) > 0 {
		// the steps first, "loop" after the last
		text, loop := "", false
		for _, h := range head {
			if h == "loop" {
				loop = true
			} else {
				text += h + "\n"
			}
		}
		text += strings.Join(inline, "\n") + "\n"
		if loop {
			text += "loop\n"
		}
		if err := rhunext.CheckMatcher(text); err != nil {
			return t, fmt.Errorf("matcher: %v", err)
		}
		t.Matchers = append(t.Matchers, rhunext.Matcher{Text: text})
	}
	if _, err := t.Encode(); err != nil {
		return t, errors.New(strings.TrimPrefix(err.(*rhunext.Error).Text, "definition: "))
	}
	return t, nil
}
