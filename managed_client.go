package main

// This adapter keeps the extension buildable with the published v1.1 SDK.
// Its wire contract also has typed helpers in the next SDK release.
import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/rhun-editor/extension-protocol/go/rhunext"
)

const managedFeatureTasks = "tasks"

type managedID int64
type managedSpec struct{ Label, Command, Workspace string }
type managedStatus struct {
	State string
	Exit  int
	Total uint64
}

const (
	managedQueued   = "queued"
	managedRunning  = "running"
	managedStopping = "stopping"
	managedExited   = "exited"
	managedFailed   = "failed"
)

func (s managedStatus) Terminal() bool { return s.State == managedExited || s.State == managedFailed }
func (s managedSpec) Validate() error {
	if len(s.Workspace) == 0 || len(s.Workspace) > 4095 || !utf8.ValidString(s.Workspace) || strings.ContainsRune(s.Workspace, 0) {
		return fmt.Errorf("invalid task workspace")
	}
	for i, value := range []string{s.Label, s.Command} {
		limit := 128
		if i == 1 {
			limit = 4096
		}
		if len(value) == 0 || len(value) > limit || !utf8.ValidString(value) {
			return fmt.Errorf("invalid task label or command")
		}
		for _, r := range value {
			if r < 0x20 || r >= 0x7f && r < 0xa0 || r >= 0x200b && r <= 0x200f || r >= 0x202a && r <= 0x202e || r == 0x2060 || r >= 0x2066 && r <= 0x2069 || r == 0xfeff {
				return fmt.Errorf("invalid task label or command")
			}
		}
	}
	return nil
}

type managedHost struct{ *rhunext.Ext }

var managedNonce atomic.Int64

func nextManagedNonce() (int64, error) {
	for {
		n := managedNonce.Load()
		if n == math.MaxInt64 {
			return 0, fmt.Errorf("task launch nonce exhausted")
		}
		if managedNonce.CompareAndSwap(n, n+1) {
			return n + 1, nil
		}
	}
}
func managedDecimal(s string, max uint64) (uint64, bool) {
	if len(s) == 0 || len(s) > 20 || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for i := range s {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil && n <= max
}
func (h managedHost) RunTask(s managedSpec) (managedID, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	nonce, err := nextManagedNonce()
	if err != nil {
		return 0, err
	}
	body := []byte(s.Workspace + s.Label + s.Command)
	reply, err := h.Request("task-run", body, strconv.FormatInt(nonce, 10), strconv.Itoa(len(s.Workspace)), strconv.Itoa(len(s.Label)), strconv.Itoa(len(body)))
	return h.handle(nonce, reply, err)
}
func (h managedHost) RestartTask(task managedID) (managedID, error) {
	if task <= 0 {
		return 0, fmt.Errorf("invalid task handle")
	}
	nonce, err := nextManagedNonce()
	if err != nil {
		return 0, err
	}
	reply, err := h.Request("task-restart", nil, strconv.FormatInt(nonce, 10), strconv.FormatInt(int64(task), 10))
	return h.handle(nonce, reply, err)
}
func (h managedHost) handle(nonce int64, reply rhunext.Reply, err error) (managedID, error) {
	if err != nil {
		return 0, err
	}
	if len(reply.Args) == 1 && len(reply.Body) == 0 {
		if id, ok := managedDecimal(reply.Args[0], math.MaxInt64); ok && id > 0 {
			return managedID(id), nil
		}
	}
	// A malformed success can still represent a launch. Cancel by nonce before
	// returning; if no bounded acknowledgement arrives, close this process's transport.
	done := make(chan bool, 1)
	go func() {
		r, e := h.Request("task-cancel", nil, strconv.FormatInt(nonce, 10))
		done <- e == nil && len(r.Args) == 0 && len(r.Body) == 0
	}()
	retire := false
	select {
	case ok := <-done:
		retire = !ok
	case <-time.After(time.Second):
		retire = true
	}
	if retire {
		os.Stdout.Close()
		os.Stdin.Close()
	}
	return 0, fmt.Errorf("invalid task handle reply")
}
func (h managedHost) empty(verb string, task managedID) error {
	if task <= 0 {
		return fmt.Errorf("invalid task handle")
	}
	reply, err := h.Request(verb, nil, strconv.FormatInt(int64(task), 10))
	if err != nil {
		return err
	}
	if len(reply.Args) != 0 || len(reply.Body) != 0 {
		return fmt.Errorf("invalid %s reply", verb)
	}
	return nil
}
func (h managedHost) StopTask(task managedID) error       { return h.empty("task-stop", task) }
func (h managedHost) ShowTaskOutput(task managedID) error { return h.empty("task-show", task) }
func (h managedHost) TaskStatus(task managedID) (managedStatus, error) {
	if task <= 0 {
		return managedStatus{}, fmt.Errorf("invalid task handle")
	}
	reply, err := h.Request("task-status", nil, strconv.FormatInt(int64(task), 10))
	if err != nil {
		return managedStatus{}, err
	}
	return decodeManagedStatus(reply)
}
func decodeManagedStatus(reply rhunext.Reply) (managedStatus, error) {
	bad := func() (managedStatus, error) { return managedStatus{}, fmt.Errorf("invalid task status reply") }
	if len(reply.Args) != 3 || len(reply.Body) != 0 {
		return bad()
	}
	value := reply.Args[1]
	if value == "" || len(value) > 5 || value[0] == '+' || value == "-0" {
		return bad()
	}
	digits := value
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if _, ok := managedDecimal(digits, 4095); !ok {
		return bad()
	}
	exit, err := strconv.Atoi(value)
	if err != nil {
		return bad()
	}
	total, ok := managedDecimal(reply.Args[2], math.MaxUint64)
	if !ok {
		return bad()
	}
	status := managedStatus{State: reply.Args[0], Exit: exit, Total: total}
	switch status.State {
	case managedQueued, managedRunning, managedStopping:
		if exit != -1 {
			return bad()
		}
	case managedExited:
		if exit < 0 || exit > 255 {
			return bad()
		}
	case managedFailed:
		if exit >= 0 || exit < -4095 {
			return bad()
		}
	default:
		return bad()
	}
	return status, nil
}
