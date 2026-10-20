// tasks: the project's tasks for rhun's Run Task. Makefile targets, justfile recipes,
// package.json scripts, Cargo and Go commands and the project's own .rhun/tasks.ini are found
// when the project opens and published to rhun (protocol 1.1's tasks feature), which lists them,
// runs them as the user in terminal tabs after one approval per project, runs their dependencies
// first and turns their errors into problems (go, gcc, rustc and tsc matchers). The first 64 are
// also palette commands. The task files are checked every two seconds; a changed list is
// published again.
// Protocol: https://rhun.app/extensions/v1/
package main

import (
	"bytes"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

type ext struct {
	project      string
	projectEpoch uint64
	mu           sync.Mutex
	scanMu       sync.Mutex
	configTasks  []entry // the last valid .rhun/tasks.ini of this project
	configFor    string
	lastWarning  string
	tasks        []entry
	published    []byte // the last list rhun took
	registered   map[string]entry
	e            *rhunext.Ext
}

func (x *ext) Ready(e *rhunext.Ext, project string) {
	x.e = e
	x.project = project
	x.projectEpoch = 1
	x.registered = map[string]entry{}
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
		x.published = nil // rhun dropped the published list with the old project
		x.configFor = ""
		x.configTasks = nil
		x.lastWarning = ""
	}
	x.mu.Unlock()
	if changed {
		go x.rescan()
	}
}

func (x *ext) Run(e *rhunext.Ext, command string, doc rhunext.Doc) {
	name := strings.TrimPrefix(command, "tasks.")
	builtin := map[string]string{"run": "run_task", "stop": "stop_task", "restart": "restart_task", "show": "show_task"}
	if cmd, ok := builtin[name]; ok || strings.HasPrefix(name, "make_") || strings.HasPrefix(name, "npm_") ||
		strings.HasPrefix(name, "go_") || strings.HasPrefix(name, "cargo_") || strings.HasPrefix(name, "just_") ||
		strings.HasPrefix(name, "config_") {
		if !e.Has(rhunext.FeatureTasks) {
			e.Toast("Tasks 0.2 needs rhun 0.18 or later")
			return
		}
		if ok {
			if err := e.Cmd(cmd); err != nil {
				e.Logf("cmd %s: %v", cmd, err)
			}
			return
		}
		x.mu.Lock()
		t, found := x.registered[name]
		x.mu.Unlock()
		if !found {
			return
		}
		// started off the handler: the approval dialog may wait for the user
		go func() {
			if _, err := e.StartTask(t.task.ID); err != nil {
				e.Logf("start %s: %v", t.task.ID, err)
			}
		}()
		return
	}
	if name == "rescan" {
		n := x.rescan()
		e.Toast(plural(n, "task") + " found")
	}
}

// rescan reads the task files, publishes the list when it changed and registers the commands
// that are new; it returns the count.
func (x *ext) rescan() int {
	x.scanMu.Lock()
	defer x.scanMu.Unlock()
	x.mu.Lock()
	project := x.project
	epoch := x.projectEpoch
	x.mu.Unlock()
	canUnregister := x.e.Has("commands")
	var discovered taskScan
	var configured []entry
	var configErr error
	if project != "" {
		discovered = discoverTasks(project, isWindows)
		configured, configErr = readConfiguredTasks(project)
	}
	x.mu.Lock()
	if x.project != project || x.projectEpoch != epoch {
		x.mu.Unlock()
		return 0
	}
	if x.configFor != project {
		x.configFor = project
		x.configTasks = nil
	}
	if configErr == nil {
		x.configTasks = configured
	} else {
		discovered.warn("Cannot load " + taskConfigPath + ": " + configErr.Error())
	}
	// the project's own first; a discovered task with the same id gives way
	tasks := append([]entry(nil), x.configTasks...)
	own := map[string]bool{}
	for _, t := range tasks {
		own[t.task.ID] = true
	}
	for _, t := range discovered.tasks {
		if !own[t.task.ID] {
			tasks = append(tasks, t)
		}
	}
	x.tasks = tasks
	x.mu.Unlock()

	x.publish(tasks, epoch, &discovered)

	x.mu.Lock()
	var fresh []entry
	desired := tasks
	if len(desired) > 64 {
		desired = desired[:64]
		discovered.warn(taskRegistrationWarning(len(tasks)))
	}
	present := make(map[string]bool, len(desired))
	for _, t := range desired {
		present[t.name] = true
		old, exists := x.registered[t.name]
		if !exists || canUnregister && old.task.Label != t.task.Label {
			fresh = append(fresh, t)
		}
	}
	var removed []string
	for name, old := range x.registered {
		if !present[name] {
			removed = append(removed, name)
			continue
		}
		if canUnregister {
			for _, t := range desired {
				if t.name == name && old.task.Label != t.task.Label {
					removed = append(removed, name)
					break
				}
			}
		}
	}
	// a registered command keeps running the current definition of its task
	for _, t := range desired {
		if _, ok := x.registered[t.name]; ok {
			x.registered[t.name] = t
		}
	}
	x.mu.Unlock()
	sort.Strings(removed)
	if canUnregister {
		for _, name := range removed {
			if err := x.e.Unregister(name); err != nil {
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
		if err := x.e.Register(t.name, "", t.task.Label); err != nil {
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

// publish sends the list to rhun when it differs from what rhun has; a list rhun refuses (a
// depends naming a task that is gone) goes again without the project's own tasks
func (x *ext) publish(tasks []entry, epoch uint64, scan *taskScan) {
	if !x.e.Has(rhunext.FeatureTasks) {
		return
	}
	list := make([]rhunext.Task, 0, len(tasks))
	for _, t := range tasks {
		if len(list) == 1000 {
			break
		}
		list = append(list, t.task)
	}
	body, err := rhunext.EncodeTaskList(list)
	if err != nil {
		scan.warn(strings.TrimPrefix(err.Error(), "rhun: "))
		list = list[:0]
		for _, t := range tasks {
			if !strings.HasPrefix(t.name, "config_") && len(list) < 1000 {
				list = append(list, t.task)
			}
		}
		if body, err = rhunext.EncodeTaskList(list); err != nil {
			return
		}
	}
	x.mu.Lock()
	same := bytes.Equal(body, x.published) || x.projectEpoch != epoch
	x.mu.Unlock()
	if same {
		return
	}
	if err := x.e.SetTasks(list); err != nil {
		x.e.Logf("tasks-set: %v", err)
		scan.warn(strings.TrimPrefix(err.Error(), "rhun: "))
		return
	}
	x.mu.Lock()
	x.published = body
	x.mu.Unlock()
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
	features := []string{"commands", rhunext.FeatureTerminals, rhunext.FeatureTasks}
	if err := rhunext.RunWith(features, &ext{}); err != nil {
		os.Exit(1)
	}
}
