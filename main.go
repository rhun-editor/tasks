// tasks: the project's tasks in rhun's palette. Makefile targets, package.json scripts,
// justfile recipes, Cargo and Go commands are found when the project opens, each registered as
// a command "make build", "npm run lint", …; running one proposes the command line for rhun's
// terminal (Enter runs it). Run Task… (ctrl+alt+r) lists them all. The task files are checked
// every two seconds: new tasks join the palette (a task that disappears stays until the
// extension restarts, as the protocol has no unregister).
// Protocol: https://rhun.app/extensions/v1/
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

type task struct {
	name    string // the command's name part: make_build
	title   string // make build
	command string // the shell line
}

type ext struct {
	project    string
	mu         sync.Mutex
	tasks      []task
	registered map[string]bool
	stamps     map[string]time.Time
	e          *rhunext.Ext
}

var taskFiles = []string{"Makefile", "makefile", "GNUmakefile", "package.json", "justfile", "Justfile", ".justfile", "Cargo.toml", "go.mod"}

func (x *ext) Ready(e *rhunext.Ext, project string) {
	x.e = e
	x.project = project
	x.registered = map[string]bool{}
	x.stamps = map[string]time.Time{}
	if project == "" {
		return
	}
	x.rescan()
	go x.watch()
}

func (x *ext) Event(e *rhunext.Ext, ev rhunext.Event) {
	if ev.Name == "project" && len(ev.Args) > 0 && ev.Args[0] != "-" && ev.Args[0] != x.project {
		// rhun starts "on = project" extensions again for a new project; this process goes
		x.mu.Lock()
		x.project = ev.Args[0]
		x.mu.Unlock()
	}
}

func (x *ext) Run(e *rhunext.Ext, command string, doc rhunext.Doc) {
	name := strings.TrimPrefix(command, "tasks.")
	switch name {
	case "rescan":
		n := x.rescan()
		e.Toast(plural(n, "task") + " found")
		return
	case "run":
		x.mu.Lock()
		tasks := append([]task(nil), x.tasks...)
		x.mu.Unlock()
		if len(tasks) == 0 {
			e.Toast("Tasks: nothing found (Makefile, package.json, justfile, Cargo.toml, go.mod)")
			return
		}
		items := make([]string, len(tasks))
		for i, t := range tasks {
			items[i] = t.title
		}
		n, err := e.Choose("Run a task", items)
		if err != nil {
			if !rhunext.Cancelled(err) {
				e.Toast("Tasks: " + err.Error())
			}
			return
		}
		x.propose(tasks[n])
		return
	}
	x.mu.Lock()
	var found *task
	for i := range x.tasks {
		if x.tasks[i].name == name {
			found = &x.tasks[i]
		}
	}
	x.mu.Unlock()
	if found != nil {
		x.propose(*found)
	}
}

func (x *ext) propose(t task) {
	if err := x.e.Term(t.command); err != nil && !rhunext.Cancelled(err) {
		x.e.Toast("Tasks: " + err.Error())
	}
}

// rescan reads the task files and registers the commands that are new; it returns the count.
func (x *ext) rescan() int {
	x.mu.Lock()
	project := x.project
	x.mu.Unlock()
	tasks := scan(project)
	x.mu.Lock()
	x.tasks = tasks
	for _, f := range taskFiles {
		if st, err := os.Stat(filepath.Join(project, f)); err == nil {
			x.stamps[f] = st.ModTime()
		} else {
			delete(x.stamps, f)
		}
	}
	var fresh []task
	for _, t := range tasks {
		if !x.registered[t.name] {
			x.registered[t.name] = true
			fresh = append(fresh, t)
		}
	}
	x.mu.Unlock()
	for _, t := range fresh {
		if err := x.e.Register(t.name, "", t.title); err != nil {
			x.e.Logf("register %s: %v", t.name, err)
		}
	}
	return len(tasks)
}

func (x *ext) watch() {
	for {
		time.Sleep(2 * time.Second)
		x.mu.Lock()
		project := x.project
		changed := false
		for _, f := range taskFiles {
			st, err := os.Stat(filepath.Join(project, f))
			old, had := x.stamps[f]
			if (err == nil) != had || (err == nil && !st.ModTime().Equal(old)) {
				changed = true
			}
		}
		x.mu.Unlock()
		if changed {
			x.rescan()
		}
	}
}

// scan finds the tasks in a project folder, in a stable order: make, just, npm, cargo, go.
func scan(project string) []task {
	var out []task
	seen := map[string]bool{}
	add := func(kind, what, title, command string) {
		name := kind + "_" + slug(what)
		for seen[name] {
			name += "_"
		}
		seen[name] = true
		out = append(out, task{name: name, title: title, command: command})
	}
	for _, f := range []string{"Makefile", "makefile", "GNUmakefile"} {
		if targets := makeTargets(filepath.Join(project, f)); targets != nil {
			for _, t := range targets {
				add("make", t, "make "+t, "make "+t)
			}
			break
		}
	}
	for _, f := range []string{"justfile", "Justfile", ".justfile"} {
		if recipes := justRecipes(filepath.Join(project, f)); recipes != nil {
			for _, r := range recipes {
				add("just", r, "just "+r, "just "+r)
			}
			break
		}
	}
	if scripts := npmScripts(filepath.Join(project, "package.json")); scripts != nil {
		runner := "npm run"
		if exists(filepath.Join(project, "pnpm-lock.yaml")) {
			runner = "pnpm"
		} else if exists(filepath.Join(project, "yarn.lock")) {
			runner = "yarn"
		} else if exists(filepath.Join(project, "bun.lockb")) || exists(filepath.Join(project, "bun.lock")) {
			runner = "bun run"
		}
		for _, s := range scripts {
			add("npm", s, "npm run "+s, runner+" "+s)
		}
	}
	if exists(filepath.Join(project, "Cargo.toml")) {
		for _, c := range []string{"build", "test", "run", "check", "clippy"} {
			add("cargo", c, "cargo "+c, "cargo "+c)
		}
	}
	if exists(filepath.Join(project, "go.mod")) {
		for _, c := range []string{"build ./...", "test ./...", "vet ./...", "run ."} {
			add("go", strings.Fields(c)[0], "go "+c, "go "+c)
		}
	}
	return out
}

var targetLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_./-]*)\s*:([^=]|$)`)

func makeTargets(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := []string{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		m := targetLine.FindStringSubmatch(line)
		if m == nil || strings.Contains(line, "%") || strings.HasPrefix(line, "\t") {
			continue
		}
		if m[1] == "" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

var recipeLine = regexp.MustCompile(`^@?([A-Za-z_][A-Za-z0-9_-]*)(\s+[^:]*)?:(\s|$)`)

func justRecipes(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") ||
			strings.Contains(line, ":=") || strings.HasPrefix(line, "set ") || strings.HasPrefix(line, "import") ||
			strings.HasPrefix(line, "mod ") || strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "alias ") {
			continue
		}
		if m := recipeLine.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], "_") {
			out = append(out, m[1])
		}
	}
	return out
}

func npmScripts(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil || len(pkg.Scripts) == 0 {
		return nil
	}
	out := make([]string, 0, len(pkg.Scripts))
	for k := range pkg.Scripts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// slug makes a command name part: lower case, [a-z0-9_], runs of anything else as one _, at
// most 30 bytes
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
		}
		if b.Len() >= 30 {
			break
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "task"
	}
	return out
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return itoa(n) + " " + what + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func main() {
	if err := rhunext.Run(&ext{}); err != nil {
		os.Exit(1)
	}
}
