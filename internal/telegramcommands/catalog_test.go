package telegramcommands

import (
	"strings"
	"testing"
)

func TestDispatchResolvesSupportedCommands(t *testing.T) {
	for _, test := range []struct {
		text string
		name string
	}{
		{text: "/help", name: "help"},
		{text: "/m terra", name: "m"},
		{text: "/getenv", name: "getenv"},
	} {
		command, ok := Dispatch(test.text)
		if !ok || command.Name != test.name {
			t.Errorf("Dispatch(%q) = %+v, %v; want %q", test.text, command, ok, test.name)
		}
	}
	for _, text := range []string{"", "hello", "/more", "/help@other_bot", "/get_env", "/get_migration", "/get" + "-" + "env"} {
		if _, ok := Dispatch(text); ok {
			t.Errorf("Dispatch(%q) unexpectedly matched", text)
		}
	}
}

func TestHelpContainsOnlyOneConciseLinePerCommand(t *testing.T) {
	help := HelpText()
	lines := strings.Split(help, "\n")
	commands := Catalog()
	if len(lines) != len(commands) {
		t.Fatalf("help has %d lines for %d commands: %q", len(lines), len(commands), help)
	}
	for index, command := range commands {
		want := command.Usage + ": " + command.HelpDescription
		if lines[index] != want || !strings.HasSuffix(command.HelpDescription, ".") {
			t.Errorf("help line %d = %q, want %q", index, lines[index], want)
		}
		if command.Access == "" {
			t.Errorf("command %s omitted access requirements", command.Name)
		}
	}
	for _, forbidden := range []string{"Available Telegram commands:", "paired account", "private chat", "/get_env", "/get_migration", "—"} {
		if strings.Contains(help, forbidden) {
			t.Errorf("help contains non-command or repetitive access text %q: %q", forbidden, help)
		}
	}
}

func TestMenuIncludesSupportedCommands(t *testing.T) {
	want := []string{"help", "stop", "restart", "login", "start", "pair", "clear", "m", "env", "getenv"}
	menu := Menu()
	if len(menu) != len(want) {
		t.Fatalf("menu count = %d, want %d: %#v", len(menu), len(want), menu)
	}
	for index, command := range menu {
		if command.Command != want[index] || strings.Contains(command.Command, "-") {
			t.Errorf("menu[%d] = %+v, want command %q", index, command, want[index])
		}
		if command.Description == "" || len(command.Description) > 256 {
			t.Errorf("menu command %q has an invalid description", command.Command)
		}
	}
	if menu[len(menu)-1].Command != "getenv" || menu[len(menu)-1].Description != "Download saved environment settings" {
		t.Fatalf("getenv menu entry = %+v", menu[len(menu)-1])
	}
}

func TestReferenceContainsCanonicalCommandsAndAccessRequirements(t *testing.T) {
	reference := Reference()
	for _, want := range []string{
		"## /login",
		"Access requirements: Paired account only, private chat only.",
		"## /getenv",
		"Primary command: `/getenv`",
		"Aliases: none",
		"Access requirements: Paired account only, private chat only.",
		"Native Telegram menu: yes",
		"Pairing requires a private chat",
		"After pairing, the paired account can use /pair in a private chat, group, or supergroup for readiness guidance",
		"/start CODE is ignored",
		"/start CODE never pairs or forwards the code",
		"Native Telegram menu: yes",
		"running GoAster binary for this request",
	} {
		if !strings.Contains(reference, want) {
			t.Errorf("reference omitted %q", want)
		}
	}
	for _, forbidden := range []string{"/get_env", "/get_migration", "telegram-commands.md", "github.com", "http://", "https://"} {
		if strings.Contains(reference, forbidden) {
			t.Errorf("reference contains obsolete or external text %q", forbidden)
		}
	}
}
