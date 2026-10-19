package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type fakeTaskClient struct {
	managed    bool
	specs      []managedSpec
	calls      []string
	choose     func([]string) int
	run        func()
	statuses   map[managedID]managedStatus
	statusHook func()
}

func (f *fakeTaskClient) Has(feature string) bool {
	return feature == managedFeatureTasks && f.managed
}
func (f *fakeTaskClient) RunTask(s managedSpec) (managedID, error) {
	f.specs = append(f.specs, s)
	if f.run != nil {
		f.run()
	}
	return 91, nil
}
func (f *fakeTaskClient) RestartTask(task managedID) (managedID, error) {
	f.calls = append(f.calls, fmt.Sprint("restart ", task))
	return 92, nil
}
func (f *fakeTaskClient) StopTask(task managedID) error {
	f.calls = append(f.calls, fmt.Sprint("stop ", task))
	return nil
}
func (f *fakeTaskClient) ShowTaskOutput(task managedID) error {
	f.calls = append(f.calls, fmt.Sprint("show ", task))
	return nil
}
func (f *fakeTaskClient) TaskStatus(task managedID) (managedStatus, error) {
	if f.statusHook != nil {
		f.statusHook()
	}
	return f.statuses[task], nil
}
func (f *fakeTaskClient) Term(command string) error {
	f.calls = append(f.calls, "term "+command)
	return nil
}
func (f *fakeTaskClient) Toast(message string) error {
	f.calls = append(f.calls, "toast "+message)
	return nil
}
func (f *fakeTaskClient) Choose(prompt string, items []string) (int, error) {
	if f.choose != nil {
		return f.choose(items), nil
	}
	return 0, nil
}
func TestManagedRunAndOldHostFallback(t *testing.T) {
	for _, managed := range []bool{false, true} {
		client := &fakeTaskClient{managed: managed}
		x := ext{project: "/project", projectEpoch: 1, client: client}
		selected := task{title: "Build", command: "go build ./..."}
		x.propose(selected)
		if managed {
			if !reflect.DeepEqual(client.specs, []managedSpec{{Label: "Build", Command: "go build ./...", Workspace: "/project"}}) || !reflect.DeepEqual(client.calls, []string{"show 91"}) || len(x.runs) != 1 {
				t.Fatal(client, x.runs)
			}
		} else if !reflect.DeepEqual(client.calls, []string{"term go build ./..."}) || len(client.specs) != 0 {
			t.Fatal(client)
		}
	}
}
func TestTaskSelectionCapturedByValue(t *testing.T) {
	client := &fakeTaskClient{managed: true}
	x := ext{project: "/p", projectEpoch: 1, client: client, tasks: []task{{name: "make_build", title: "Build", command: "original"}}}
	client.choose = func(items []string) int { x.mu.Lock(); x.tasks[0].command = "changed"; x.mu.Unlock(); return 0 }
	x.runCommand("rhun-editor/tasks.run")
	if len(client.specs) != 1 || client.specs[0].Command != "original" {
		t.Fatal(client.specs)
	}
}
func TestProjectSwitchAndABACancelSelection(t *testing.T) {
	client := &fakeTaskClient{managed: true}
	x := ext{project: "/p", projectEpoch: 1, client: client, tasks: []task{{title: "Build", command: "original"}}}
	client.choose = func(items []string) int { x.mu.Lock(); x.projectEpoch += 2; x.mu.Unlock(); return 0 }
	x.runCommand("tasks.run")
	if len(client.specs) != 0 {
		t.Fatal("stale selection launched")
	}
	client.run = func() { x.mu.Lock(); x.project = "/other"; x.projectEpoch++; x.mu.Unlock() }
	client.calls = nil
	x.propose(task{title: "Build", command: "original"})
	if !reflect.DeepEqual(client.calls, []string{"stop 91"}) || len(x.runs) != 0 {
		t.Fatal(client.calls, x.runs)
	}
}
func TestManagedControlsAndBoundedHistory(t *testing.T) {
	client := &fakeTaskClient{managed: true, statuses: map[managedID]managedStatus{1: {State: managedExited}, 2: {State: managedRunning}}}
	x := ext{project: "/p", projectEpoch: 1, client: client, runs: []managedTask{{handle: 1, title: "old"}, {handle: 2, title: "current"}}}
	x.manageTasks("stop")
	if !reflect.DeepEqual(client.calls, []string{"stop 2"}) {
		t.Fatal(client.calls)
	}
	client.calls = nil
	x.manageTasks("restart")
	if !reflect.DeepEqual(client.calls, []string{"restart 2", "show 92"}) {
		t.Fatal(client.calls)
	}
	client.statuses[1] = managedStatus{State: managedExited}
	client.statuses[2] = managedStatus{State: managedRunning}
	client.statuses[92] = managedStatus{State: managedExited}
	for i := 0; i < 40; i++ {
		evict, err := x.prepareHistory("/p", 1)
		if err != nil {
			t.Fatal(err)
		}
		client.statuses[managedID(i+100)] = managedStatus{State: managedExited}
		if !x.retainRun("/p", 1, managedTask{handle: managedID(i + 100), title: "x"}, evict) {
			t.Fatal("retain")
		}
	}
	if len(x.runs) != 32 || x.runs[0].handle != 2 || x.runs[1].handle != 109 || x.runs[31].handle != 139 {
		t.Fatal(x.runs)
	}
}

func TestChangedOldHostPaletteEntryCannotRunNewDefinition(t *testing.T) {
	client := &fakeTaskClient{}
	x := ext{project: "/p", projectEpoch: 1, client: client, tasks: []task{{name: "config_deploy", title: "Production", command: "deploy production"}}, registered: map[string]task{"config_deploy": {name: "config_deploy", title: "Staging", command: "deploy staging"}}}
	x.runCommand("tasks.config_deploy")
	if len(client.specs) != 0 || len(client.calls) != 1 || !strings.HasPrefix(client.calls[0], "toast ") {
		t.Fatal(client.calls)
	}
	x.runCommand("tasks.run")
	if client.calls[len(client.calls)-1] != "term deploy production" {
		t.Fatal(client.calls)
	}
}

func TestProjectChangeDuringHistoryQueryPreventsLaunch(t *testing.T) {
	client := &fakeTaskClient{managed: true, statuses: map[managedID]managedStatus{}}
	x := ext{project: "/first", projectEpoch: 1, client: client}
	for i := 0; i < 32; i++ {
		handle := managedID(i + 1)
		x.runs = append(x.runs, managedTask{handle: handle, title: "old"})
		client.statuses[handle] = managedStatus{State: managedExited}
	}
	client.statusHook = func() { x.mu.Lock(); x.project = "/second"; x.projectEpoch++; x.mu.Unlock() }
	x.propose(task{title: "Selected in first", command: "echo first"})
	if len(client.specs) != 0 {
		t.Fatal("launched after project changed during history query", client.specs)
	}
}
