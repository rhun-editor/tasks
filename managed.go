package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

type taskClient interface {
	Has(string) bool
	RunTask(managedSpec) (managedID, error)
	RestartTask(managedID) (managedID, error)
	StopTask(managedID) error
	TaskStatus(managedID) (managedStatus, error)
	ShowTaskOutput(managedID) error
	Term(string) error
	Choose(string, []string) (int, error)
	Toast(string) error
}
type managedTask struct {
	handle managedID
	title  string
}

func (x *ext) taskClient() taskClient {
	if x.client != nil {
		return x.client
	}
	return managedHost{x.e}
}
func taskCommandKey(command string) string {
	if i := strings.LastIndexByte(command, '.'); i >= 0 {
		return command[i+1:]
	}
	return command
}

func (x *ext) Run(e *rhunext.Ext, command string, doc rhunext.Doc) {
	// Prompts must not hold the SDK event handler while project/bye events accumulate.
	go x.runCommand(command)
}
func (x *ext) runCommand(command string) {
	client := x.taskClient()
	if !x.actionMu.TryLock() {
		client.Toast("Tasks: another task action is open")
		return
	}
	defer x.actionMu.Unlock()
	name := taskCommandKey(command)
	switch name {
	case "rescan":
		n := x.rescan()
		client.Toast(plural(n, "task") + " found")
		return
	case "stop", "restart", "output":
		x.manageTasks(name)
		return
	case "run":
		x.mu.Lock()
		tasks := append([]task(nil), x.tasks...)
		project, epoch := x.project, x.projectEpoch
		x.mu.Unlock()
		if len(tasks) == 0 {
			client.Toast("Tasks: nothing found (.rhun/tasks.json, Makefile, package.json, justfile, Cargo.toml, go.mod)")
			return
		}
		items := make([]string, len(tasks))
		for i, t := range tasks {
			items[i] = t.title
		}
		n, err := client.Choose("Run a task", items)
		if err != nil {
			if !rhunext.Cancelled(err) {
				client.Toast("Tasks: " + err.Error())
			}
			return
		}
		if n < 0 || n >= len(tasks) {
			client.Toast("Tasks: invalid task selection")
			return
		}
		x.proposeAt(tasks[n], project, epoch)
		return
	}
	x.mu.Lock()
	project, epoch := x.project, x.projectEpoch
	var selected task
	found := false
	for _, candidate := range x.tasks {
		if candidate.name == name {
			if old, ok := x.registered[name]; ok && old.title != candidate.title {
				x.mu.Unlock()
				client.Toast("Tasks: this task changed; use Run Task to select its current definition")
				return
			}
			selected = candidate
			found = true
			break
		}
	}
	x.mu.Unlock()
	if found {
		x.proposeAt(selected, project, epoch)
	} else {
		client.Toast("Tasks: that task is no longer available")
	}
}
func (x *ext) taskError(err error) {
	if err != nil && !rhunext.Cancelled(err) {
		x.taskClient().Toast("Tasks: " + taskWarningText(err.Error()))
	}
}
func (x *ext) propose(t task) {
	x.mu.Lock()
	project, epoch := x.project, x.projectEpoch
	x.mu.Unlock()
	x.proposeAt(t, project, epoch)
}
func (x *ext) proposeAt(t task, project string, epoch uint64) {
	client := x.taskClient()
	x.mu.Lock()
	stale := x.project != project || x.projectEpoch != epoch
	x.mu.Unlock()
	if stale {
		client.Toast("Tasks: project changed while selecting a task")
		return
	}
	if !client.Has(managedFeatureTasks) {
		x.taskError(client.Term(t.command))
		return
	}
	evict, err := x.prepareHistory(project, epoch)
	if err != nil {
		x.taskError(err)
		return
	}
	x.mu.Lock()
	stale = x.project != project || x.projectEpoch != epoch
	x.mu.Unlock()
	if stale {
		client.Toast("Tasks: project changed while selecting a task")
		return
	}
	handle, err := client.RunTask(managedSpec{Label: t.title, Command: t.command, Workspace: project})
	if err != nil {
		x.taskError(err)
		return
	}
	if !x.retainRun(project, epoch, managedTask{handle: handle, title: t.title}, evict) {
		x.taskError(client.StopTask(handle))
		return
	}
	x.taskError(client.ShowTaskOutput(handle))
}

// Reserve a completed history slot before launching. Never evict a live task.
func (x *ext) prepareHistory(project string, epoch uint64) (managedID, error) {
	x.mu.Lock()
	runs := append([]managedTask(nil), x.runs...)
	stale := x.project != project || x.projectEpoch != epoch
	x.mu.Unlock()
	if stale {
		return 0, fmt.Errorf("project changed while selecting a task")
	}
	if len(runs) < 32 {
		return 0, nil
	}
	for _, run := range runs {
		status, err := x.taskClient().TaskStatus(run.handle)
		var reply *rhunext.Error
		gone := errors.As(err, &reply) && reply.Code == "notask"
		if err != nil && !gone {
			return 0, err
		}
		x.mu.Lock()
		stale = x.project != project || x.projectEpoch != epoch
		x.mu.Unlock()
		if stale {
			return 0, fmt.Errorf("project changed while selecting a task")
		}
		if gone || status.Terminal() {
			return run.handle, nil
		}
	}
	return 0, fmt.Errorf("all retained tasks are active; stop a task before starting another")
}
func (x *ext) retainRun(project string, epoch uint64, run managedTask, evict managedID) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.project != project || x.projectEpoch != epoch {
		return false
	}
	if len(x.runs) >= 32 {
		index := -1
		for i, old := range x.runs {
			if old.handle == evict {
				index = i
				break
			}
		}
		if index < 0 {
			return false
		}
		copy(x.runs[index:], x.runs[index+1:])
		x.runs = x.runs[:len(x.runs)-1]
	}
	x.runs = append(x.runs, run)
	return true
}
func (x *ext) manageTasks(action string) {
	client := x.taskClient()
	if !client.Has(managedFeatureTasks) {
		client.Toast("Tasks: managed tasks are unavailable on this rhun build")
		return
	}
	x.mu.Lock()
	runs := append([]managedTask(nil), x.runs...)
	project, epoch := x.project, x.projectEpoch
	x.mu.Unlock()
	var choices []managedTask
	var items []string
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		status, err := client.TaskStatus(run.handle)
		if err != nil {
			var reply *rhunext.Error
			if errors.As(err, &reply) && reply.Code == "notask" {
				continue
			}
			x.taskError(err)
			return
		}
		if action == "stop" && status.Terminal() {
			continue
		}
		choices = append(choices, run)
		items = append(items, fmt.Sprintf("%s (%s)", run.title, status.State))
	}
	if len(choices) == 0 {
		client.Toast("Tasks: no matching managed runs")
		return
	}
	title := map[string]string{"stop": "Stop a task", "restart": "Restart a task", "output": "Show task output"}[action]
	n, err := client.Choose(title, items)
	if err != nil {
		x.taskError(err)
		return
	}
	if n < 0 || n >= len(choices) {
		client.Toast("Tasks: invalid task selection")
		return
	}
	selected := choices[n]
	x.mu.Lock()
	stale := x.projectEpoch != epoch
	x.mu.Unlock()
	if stale {
		client.Toast("Tasks: project changed while selecting a task")
		return
	}
	switch action {
	case "stop":
		x.taskError(client.StopTask(selected.handle))
	case "output":
		x.taskError(client.ShowTaskOutput(selected.handle))
	case "restart":
		evict, err := x.prepareHistory(project, epoch)
		if err != nil {
			x.taskError(err)
			return
		}
		handle, err := client.RestartTask(selected.handle)
		if err != nil {
			x.taskError(err)
			return
		}
		if !x.retainRun(project, epoch, managedTask{handle: handle, title: selected.title}, evict) {
			x.taskError(client.StopTask(handle))
			return
		}
		x.taskError(client.ShowTaskOutput(handle))
	}
}
