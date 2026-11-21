package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var evidenceExitLine = regexp.MustCompile(`(?m)^exit_code: ([0-9]{1,3})\z`)

// Arguments are inspected only to select a conservative output policy; they are never saved.
func toolEvidenceNote(name, arguments string, completed bool, output string) string {
	name = safeEvidenceName(name)
	if !completed {
		return "[Untrusted tool evidence: " + name + " started; outcome uncertain. Arguments omitted.]"
	}
	detail := "Output omitted; arguments omitted."
	if (name == "write_file" && strings.HasPrefix(output, "File written: ")) ||
		(name == "edit_file" && strings.HasPrefix(output, "Edited one occurrence in ")) ||
		(name == "append_file" && strings.HasPrefix(output, "Appended to ")) {
		detail = "File operation succeeded; path and arguments omitted."
	}
	if name == "write_file" || name == "edit_file" || name == "append_file" {
		switch {
		case strings.HasPrefix(output, name+": operation committed, but durability is uncertain:"):
			detail = "File operation committed; durability uncertain; output and arguments omitted."
		case strings.HasPrefix(output, name+" error: "):
			detail = "File operation failed; output and arguments omitted."
		}
	}
	if name == "shell_execute" {
		args, err := parseShellExecuteArguments(arguments)
		if err == nil && len(args.Env) == 0 {
			command := strings.TrimSpace(args.Command)
			git := command == "git status" || command == "git status --short" || command == "git status -sb"
			build := command == "go build" || command == "go build ./..." || command == "go test ./..."
			if git || build {
				status := []string{}
				match := evidenceExitLine.FindStringSubmatch(output)
				if len(match) == 2 {
					code, err := strconv.Atoi(match[1])
					if err == nil && code <= 255 {
						status = append(status, fmt.Sprintf("exit code: %d", code))
						if git && code == 0 && strings.HasPrefix(output, "stdout:\n") && strings.Contains(output, "\nstderr:\n") && !strings.Contains(output, "\nstdout_truncated: true\n") {
							stdout, _, _ := strings.Cut(strings.TrimPrefix(output, "stdout:\n"), "\nstderr:\n")
							lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
							if command == "git status" {
								for _, line := range lines {
									if line == "nothing to commit, working tree clean" {
										status = append(status, "git clean")
										break
									}
								}
							} else if stdout == "" || stdout == "\n" || (command == "git status -sb" && len(lines) == 1 && strings.HasPrefix(lines[0], "## ")) {
								status = append(status, "git clean")
							} else if command == "git status --short" || (command == "git status -sb" && len(lines) > 1 && strings.HasPrefix(lines[0], "## ")) {
								status = append(status, "git dirty")
							}
						}
						if build && code != 0 {
							if command == "go test ./..." && strings.Contains(output, "FAIL") {
								status = append(status, "test failed")
							} else {
								status = append(status, "build failed")
							}
						}
					}
				}
				if len(status) > 0 {
					detail = strings.Join(status, "; ") + "; output and arguments omitted."
				}
			}
		}
	}
	return fmt.Sprintf("[Untrusted tool evidence: %s completed; handler returned output (%d bytes). This completion supersedes the preceding start for this action; it does not establish operation success. %s]", name, len(output), detail)
}

func safeEvidenceName(name string) string {
	if len(name) == 0 || len(name) > 64 {
		return "unknown tool"
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "unknown tool"
		}
	}
	return name
}
