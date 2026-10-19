package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"unicode/utf8"
)

const maxTaskConfigBytes = 64 << 10
const maxConfiguredTasks = 128
const taskConfigPath = ".rhun/tasks.json"

func readConfiguredTasks(project string) ([]task, error) {
	data, err := readTaskSource(project, taskConfigPath, maxTaskConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeTaskConfig(data)
}
func taskObject(d *json.Decoder, allowed map[string]bool) (map[string]json.RawMessage, error) {
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("expected task configuration object")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok || !allowed[name] {
			return nil, fmt.Errorf("unsupported task field %q", name)
		}
		if _, ok := fields[name]; ok {
			return nil, fmt.Errorf("duplicate task field %q", name)
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("unfinished task object")
	}
	return fields, nil
}
func taskJSONEnd(d *json.Decoder) error {
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing task configuration data")
	}
	return nil
}
func decodeTaskConfig(data []byte) ([]task, error) {
	if len(data) > maxTaskConfigBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("task configuration is too large or invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	fields, err := taskObject(d, map[string]bool{"version": true, "tasks": true})
	if err != nil {
		return nil, err
	}
	if err := taskJSONEnd(d); err != nil {
		return nil, err
	}
	if string(fields["version"]) != "1" {
		return nil, fmt.Errorf("task configuration version must be 1")
	}
	list := json.NewDecoder(bytes.NewReader(fields["tasks"]))
	token, err := list.Token()
	if err != nil || token != json.Delim('[') {
		return nil, fmt.Errorf("tasks must be an array")
	}
	out := []task{}
	seen := map[string]bool{}
	for list.More() {
		if len(out) == maxConfiguredTasks {
			return nil, fmt.Errorf("task configuration exceeds %d tasks", maxConfiguredTasks)
		}
		entry, err := taskObject(list, map[string]bool{"id": true, "label": true, "command": true})
		if err != nil {
			return nil, err
		}
		id, err := taskJSONString(entry["id"])
		if err != nil {
			return nil, err
		}
		if len(id) == 0 || len(id) > 33 {
			return nil, fmt.Errorf("task id must have 1 to 33 ASCII characters")
		}
		for i := range id {
			if !(id[i] >= 'a' && id[i] <= 'z' || id[i] >= '0' && id[i] <= '9' || id[i] == '_') {
				return nil, fmt.Errorf("invalid task id")
			}
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate task id %q", id)
		}
		seen[id] = true
		label, err := taskJSONString(entry["label"])
		if err != nil {
			return nil, err
		}
		command, err := taskJSONString(entry["command"])
		if err != nil {
			return nil, err
		}
		if err := (managedSpec{Label: label, Command: command, Workspace: "."}).Validate(); err != nil {
			return nil, err
		}
		out = append(out, task{name: "config_" + id, title: label, command: command})
	}
	token, err = list.Token()
	if err != nil || token != json.Delim(']') {
		return nil, fmt.Errorf("unfinished task list")
	}
	if err := taskJSONEnd(list); err != nil {
		return nil, err
	}
	return out, nil
}

// encoding/json replaces lone UTF-16 escapes. Reject those rather than silently changing a command.
func taskJSONString(raw json.RawMessage) (string, error) {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return "", fmt.Errorf("invalid task string escape")
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return "", fmt.Errorf("invalid task string escape")
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return "", fmt.Errorf("unpaired task string surrogate")
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", fmt.Errorf("unpaired task string surrogate")
			}
			next, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || next < 0xdc00 || next > 0xdfff {
				return "", fmt.Errorf("unpaired task string surrogate")
			}
			i += 6
		}
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("task fields must be strings")
	}
	return out, nil
}
