package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"goaster/internal/telegramcommands"
)

func TestAddressedCommandNormalization(t *testing.T) {
	botUsername := "ExampleBot"
	for _, command := range telegramcommands.Catalog() {
		for _, name := range append([]string{command.Name}, command.Aliases...) {
			input := " \t/" + name + "@eXAMPLEbOT  arg @someone\n"
			m := message{Text: input}
			m, keep := normalizeMessage(m, botUsername)
			if !keep || m.Text != " \t/"+name+"  arg @someone\n" {
				t.Errorf("normalization of %q = %q", input, m.Text)
			}
			m.Text = "/" + name + "@other_bot"
			_, keep = normalizeMessage(m, botUsername)
			if keep {
				t.Errorf("accepted other bot: %q", m.Text)
			}
		}
	}
	for _, input := range []string{"/m model@host", "hello /m@ExampleBot", "/unknown@ExampleBot arg", "/m@ arg", "/m@ExampleBot! arg", "/m@@ExampleBot", "/M@ExampleBot", "/m"} {
		m := message{Text: input}
		m, keep := normalizeMessage(m, botUsername)
		if !keep || m.Text != input {
			t.Errorf("changed ordinary or malformed text %q to %q", input, m.Text)
		}
	}
	m := message{Caption: "  /env@EXAMPLEBOT", Document: &document{}}
	m, keep := normalizeMessage(m, botUsername)
	if !keep || m.Caption != "  /env" {
		t.Errorf("caption = %q", m.Caption)
	}
	m.Caption = "/unknown@other_bot x"
	_, keep = normalizeMessage(m, botUsername)
	if keep {
		t.Error("unknown command addressed elsewhere accepted")
	}
}

func TestAddressedCommandsRespectAuthorizationAndAvoidAgent(t *testing.T) {
	var sent, agent int
	var forwarded string
	a := adapter{botUsername: "ExampleBot", pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
		client: doerFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/sendMessage") {
				sent++
			}
			return response(200, `{"ok":true,"result":{}}`), nil
		}),
		agent: doerFunc(func(r *http.Request) (*http.Response, error) {
			agent++
			var payload struct {
				Message string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			forwarded = payload.Message
			return response(200, "{\"type\":\"final\",\"reply\":\"ok\"}\n"), nil
		}),
	}
	for _, input := range []string{"/help@other_bot", "/m@other_bot model", "/unknown@other_bot arg", "/env@other_bot", "/restart@other_bot"} {
		a.handle(context.Background(), privateMessage(42, input))
	}
	a.handle(context.Background(), privateMessage(99, "/help@ExampleBot"))
	a.handle(context.Background(), textMessage(42, 99, "private", "/help@ExampleBot"))
	if sent != 0 || agent != 0 {
		t.Fatalf("ignored or unauthorized input: sent=%d agent=%d", sent, agent)
	}
	a.handle(context.Background(), privateMessage(42, " /help@eXAMPLEbOT "))
	if sent != 1 || agent != 0 {
		t.Fatalf("own help: sent=%d agent=%d", sent, agent)
	}
	a.handle(context.Background(), privateMessage(42, "  /m@EXAMPLEBOT model@host  "))
	if agent != 1 || forwarded != "/m model@host" {
		t.Fatalf("model command: calls=%d text=%q", agent, forwarded)
	}
	a.handle(context.Background(), privateMessage(42, "hello @other_bot"))
	if agent != 2 {
		t.Fatalf("ordinary text agent calls = %d", agent)
	}
}

func TestBotIdentityValidation(t *testing.T) {
	for _, tc := range []struct {
		isBot    bool
		username string
		valid    bool
	}{
		{true, "ExampleBot", true}, {false, "ExampleBot", false}, {true, "", false}, {true, "bad name", false}, {true, "bad@name", false},
	} {
		if (validateBotIdentity(tc.isBot, tc.username) == nil) != tc.valid {
			t.Errorf("identity validation for bot=%v username=%q", tc.isBot, tc.username)
		}
	}
}

func TestAddressedGroupCommandsAndPrivateGuards(t *testing.T) {
	for _, chatType := range []string{"group", "supergroup"} {
		t.Run(chatType, func(t *testing.T) {
			var sent []string
			var forwarded []string
			a := adapter{botUsername: "ExampleBot", pairing: &pairing{userID: 42}, apiBase: "https://example.invalid",
				client: doerFunc(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/sendMessage") {
						var p struct {
							Text string `json:"text"`
						}
						_ = json.NewDecoder(r.Body).Decode(&p)
						sent = append(sent, p.Text)
					}
					if strings.HasSuffix(r.URL.Path, "/sendDocument") {
						t.Error("private environment file sent to group")
					}
					return response(200, `{"ok":true,"result":{}}`), nil
				}),
				agent: doerFunc(func(r *http.Request) (*http.Response, error) {
					var p struct {
						Message string `json:"message"`
					}
					_ = json.NewDecoder(r.Body).Decode(&p)
					forwarded = append(forwarded, p.Message)
					return response(200, "{\"type\":\"final\",\"reply\":\"ok\"}\n"), nil
				}),
			}
			for _, text := range []string{"/help@ExampleBot", "/m@ExampleBot model", "/env@ExampleBot", "/getenv@ExampleBot"} {
				m := textMessage(42, -10, chatType, text)
				m.MessageThreadID = 17
				a.handle(context.Background(), m)
			}
			for _, text := range []string{"/help@ExampleBot", "/m@ExampleBot model", "/env@ExampleBot", "/getenv@ExampleBot"} {
				a.handle(context.Background(), textMessage(99, -10, chatType, text))
			}
			if len(forwarded) != 1 || forwarded[0] != "/m model" {
				t.Fatalf("forwarded = %q", forwarded)
			}
			if len(sent) != 4 || !strings.Contains(sent[2], "private chat") || !strings.Contains(sent[3], "private chat") {
				t.Fatalf("replies = %q", sent)
			}
		})
	}
}

func TestLoadBotIdentityFailureDiagnostics(t *testing.T) {
	cause := errors.New("transport failure")
	for _, tc := range []struct {
		name, body string
		transport  bool
	}{
		{name: "invalid identity", body: `{"ok":true,"result":{"is_bot":false,"username":"secret username"}}`},
		{name: "missing identity", body: `{"ok":true,"result":null}`},
		{name: "transport", transport: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := adapter{apiBase: "https://example.invalid", client: doerFunc(func(*http.Request) (*http.Response, error) {
				if tc.transport {
					return nil, cause
				}
				return response(200, tc.body), nil
			})}
			var err error
			logs := captureTelegramDiagnostics(t, func() { err = a.loadBotIdentity(context.Background()) })
			if err == nil || err.Error() != "Telegram bot identity could not be verified" || errors.Unwrap(err) == nil {
				t.Fatalf("error = %v", err)
			}
			if tc.transport && !errors.Is(err, cause) {
				t.Fatalf("lost transport cause: %v", err)
			}
			if strings.Count(logs, "ERROR") != 1 || !strings.Contains(logs, "getMe") || strings.Contains(logs, "secret username") {
				t.Fatalf("diagnostics = %q", logs)
			}
		})
	}
}
