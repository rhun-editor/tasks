// tasks: the project's tasks in rhun's palette. Makefile targets, package.json scripts,
// justfile recipes, Cargo and Go commands are found when the project opens, each registered as
// a command "make build", "npm run lint", ...; managed hosts track each reviewed run,
// its output and completion. Older hosts use the terminal proposal. Run Task… (ctrl+alt+r) lists them all. The task files are checked
// every two seconds: new tasks join the palette; with protocol 1.1's commands feature,
// removed tasks leave the palette again.
// Protocol: https://rhun.app/extensions/v1/
package main

import (
	"os"
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
	project       string
	projectEpoch  uint64
	mu            sync.Mutex
	scanMu        sync.Mutex
	actionMu      sync.Mutex
	client        taskClient
	runs          []managedTask
	configProject string
	configTasks   []task
	lastWarning   string
	tasks         []task
	registered    map[string]task
	e             *rhunext.Ext
}

func (x *ext) Ready(e *rhunext.Ext, project string) {
	x.e = e
	// Published SDK v1.1 truncates spaced Ready paths; the native host also supplies this exact value.
	if exact := os.Getenv("RHUN_PROJECT"); exact != "" {
		project = exact
	}
	x.project = project
	x.projectEpoch = 1
	x.registered = map[string]task{}
	if project != "" {
		x.rescan()
	}
	go x.watch()
}

func (x *ext) Event(e *rhunext.Ext, ev rhunext.Event) {
	if ev.Name != "project" || len(ev.Args) != 1 {
		return
	}
	project := ev.Args[0]
	if project == "-" {
		project = ""
	}
	x.mu.Lock()
	changed := project != x.project
	if changed {
		x.project = project
		x.projectEpoch++
		x.tasks = nil
		x.runs = nil
		x.configProject = ""
		x.configTasks = nil
		x.lastWarning = ""
	}
	x.mu.Unlock()
	if changed {
		go x.rescan()
	}
}

// rescan reads the task files and registers the commands that are new; it returns the count.
func (x *ext) rescan() int {
	x.scanMu.Lock()
	defer x.scanMu.Unlock()
	x.mu.Lock()
	project := x.project
	epoch := x.projectEpoch
	x.mu.Unlock()
	canUnregister := x.e.Has("commands")
	discovered := taskScan{}
	var configured []task
	var configErr error
	if project != "" {
		discovered = discoverTasks(project)
		configured, configErr = readConfiguredTasks(project)
	}
	x.mu.Lock()
	if x.project != project || x.projectEpoch != epoch {
		x.mu.Unlock()
		return 0
	}
	if x.configProject != project {
		x.configProject = project
		x.configTasks = nil
	}
	if configErr == nil {
		x.configTasks = configured
	} else {
		discovered.warn("Cannot load " + taskConfigPath + ": " + configErr.Error())
	}
	tasks := append(append([]task(nil), x.configTasks...), discovered.tasks...)
	x.tasks = tasks
	var fresh []task
	desired := tasks
	if len(desired) > 64 {
		desired = desired[:64]
		discovered.warn(taskRegistrationWarning(len(tasks)))
	}
	present := make(map[string]bool, len(desired))
	for _, t := range desired {
		present[t.name] = true
		old, exists := x.registered[t.name]
		if !exists || canUnregister && old.title != t.title {
			fresh = append(fresh, t)
		}
	}
	var removed []string
	for name := range x.registered {
		if !present[name] {
			removed = append(removed, name)
			continue
		}
		if canUnregister {
			for _, t := range desired {
				if t.name == name && x.registered[name].title != t.title {
					removed = append(removed, name)
					break
				}
			}
		}
	}
	x.mu.Unlock()
	sort.Strings(removed)
	if canUnregister {
		for _, name := range removed {
			// Request keeps the extension compatible with older SDK releases; the verb is
			// sent only after the host agrees to protocol 1.1's commands feature.
			if _, err := x.e.Request("unregister", nil, name); err != nil {
				x.e.Logf("unregister %s: %v", name, err)
			} else {
				x.mu.Lock()
				delete(x.registered, name)
				x.mu.Unlock()
			}
		}
	}
	for _, t := range fresh {
		x.mu.Lock()
		stale := x.projectEpoch != epoch
		_, stillRegistered := x.registered[t.name]
		full := len(x.registered) >= 64
		x.mu.Unlock()
		if stale {
			break
		}
		if stillRegistered {
			discovered.warn("A task command could not be refreshed; use Run Task")
			continue
		}
		if full {
			discovered.warn(taskRegistrationWarning(len(tasks)))
			break
		}
		if err := x.e.Register(t.name, "", t.title); err != nil {
			x.e.Logf("register %s: %v", t.name, err)
		} else {
			x.mu.Lock()
			x.registered[t.name] = t
			x.mu.Unlock()
		}
	}
	x.mu.Lock()
	if x.projectEpoch != epoch {
		x.mu.Unlock()
		return 0
	}
	warning := taskWarningText(discovered.warning)
	notify := warning != "" && warning != x.lastWarning
	x.lastWarning = warning
	x.mu.Unlock()
	if notify {
		x.e.Toast("Tasks: " + warning)
	}
	return len(tasks)
}

func (x *ext) watch() {
	// A bounded authoritative scan also catches atomic replacement and preserved mtimes.
	for {
		time.Sleep(2 * time.Second)
		x.rescan()
	}
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
	if err := rhunext.RunWith([]string{"commands", managedFeatureTasks}, &ext{}); err != nil {
		os.Exit(1)
	}
}
