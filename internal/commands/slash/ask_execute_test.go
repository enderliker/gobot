package slash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gobot/internal/ai"
	"gobot/internal/audit"
	"gobot/internal/lifecycle"

	"github.com/bwmarrin/discordgo"
)

const (
	testAskGuildID       = "100000000000000001"
	testAskChannelID     = "100000000000000002"
	testAskInteractionID = "100000000000000003"
	testAskAppID         = "100000000000000004"
	testAskToken         = "token-1"
	testAskOwnerID       = "100000000000000005"
	testAskUserID        = "100000000000000006"
	testAskTargetID      = "100000000000000007"
	testAskRoleID        = "100000000000000008"
)

func TestAskExecuteDeferredSuccessWithoutToolEditsOriginalPublicResponse(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	restoreManager := stubAIManager(t, &capturingAskProvider{
		result: &ai.AskResult{Text: "Hello from the model. @everyone <@123456789012345678>"},
	})
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)

		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"100000000000000009","channel_id":"` + testAskChannelID + `","content":"ok"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskUserID, Username: "requester"},
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskUserID, []string{testAskRoleID}, "Hello?"))

	recorder.waitForCount(t, 2)
	lifecycle.Wait()

	requests := recorder.snapshot()
	callback := findDiscordRequest(t, requests, http.MethodPost, "/interactions/"+testAskInteractionID+"/"+testAskToken+"/callback")
	editOriginal := findDiscordRequest(t, requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")

	var response discordgo.InteractionResponse
	if err := json.Unmarshal(callback.Body, &response); err != nil {
		t.Fatalf("unmarshal callback payload: %v", err)
	}
	if response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource {
		t.Fatalf("expected deferred response type, got %d", response.Type)
	}
	if response.Data != nil && response.Data.Flags != 0 {
		t.Fatalf("expected public deferred response, got %#v", response.Data)
	}
	if response.Data != nil && len(response.Data.Embeds) != 0 {
		t.Fatalf("expected no custom embed in deferred ack, got %#v", response.Data.Embeds)
	}

	if hasDiscordRequest(requests, http.MethodPost, "/channels/"+testAskChannelID+"/messages") {
		t.Fatal("did not expect a separate channel message for plain-text response")
	}

	var payload map[string]any
	if err := json.Unmarshal(editOriginal.Body, &payload); err != nil {
		t.Fatalf("unmarshal original edit payload: %v", err)
	}
	if got := payload["content"]; got != "Hello from the model. @everyone <@123456789012345678>" {
		t.Fatalf("unexpected plain-text response content: %#v", got)
	}
	if _, ok := payload["embeds"]; ok {
		t.Fatalf("expected no embeds in plain-text response edit, got %#v", payload["embeds"])
	}
	allowedMentionsRaw, ok := payload["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatal("expected allowed_mentions in plain-text response")
	}
	if parseRaw, ok := allowedMentionsRaw["parse"].([]any); !ok || len(parseRaw) != 0 {
		t.Fatalf("expected allowed_mentions.parse to be empty, got %#v", allowedMentionsRaw["parse"])
	}
	if usersRaw, ok := allowedMentionsRaw["users"]; ok {
		if users, ok := usersRaw.([]any); !ok || len(users) != 0 {
			t.Fatalf("expected allowed_mentions.users to be empty, got %#v", usersRaw)
		}
	}
	if rolesRaw, ok := allowedMentionsRaw["roles"]; ok {
		if roles, ok := rolesRaw.([]any); !ok || len(roles) != 0 {
			t.Fatalf("expected allowed_mentions.roles to be empty, got %#v", rolesRaw)
		}
	}
}

func TestAskExecuteToolCallUsesPublicNoticeAndPrivateConfirmation(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	restoreManager := stubAIManager(t, &capturingAskProvider{
		result: &ai.AskResult{
			ToolCalls: []*ai.ToolCall{
				{
					Tool:   "kick",
					User:   "Target User",
					Reason: "spam",
				},
			},
		},
	})
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	var auditBuf bytes.Buffer
	logger := audit.New(&auditBuf, nil)
	restoreAudit := audit.SetDefaultForTest(logger)
	defer restoreAudit()

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)

		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/guilds/"+testAskGuildID+"/members/search"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"original","content":"","flags":64}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"followup-1","content":"","flags":64}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskOwnerID, Username: "owner"},
			Roles: []string{testAskRoleID},
		},
		{
			User:  &discordgo.User{ID: testAskTargetID, Username: "targetuser", GlobalName: "Target User"},
			Nick:  "Target User",
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskOwnerID, []string{testAskRoleID}, "Kick Target User"))

	recorder.waitForCount(t, 4)

	lifecycle.Cancel()
	lifecycle.Wait()

	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("close audit logger: %v", err)
	}

	requests := recorder.snapshot()
	findDiscordRequest(t, requests, http.MethodPost, "/interactions/"+testAskInteractionID+"/"+testAskToken+"/callback")
	editOriginal := findDiscordRequest(t, requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")
	followup := findDiscordRequest(t, requests, http.MethodPost, "/webhooks/"+testAskAppID+"/"+testAskToken)

	if hasDiscordRequest(requests, http.MethodPost, "/channels/"+testAskChannelID+"/messages") {
		t.Fatal("did not expect a regular channel message for tool confirmation")
	}

	var originalPayload map[string]any
	if err := json.Unmarshal(editOriginal.Body, &originalPayload); err != nil {
		t.Fatalf("unmarshal original edit payload: %v", err)
	}
	if got := originalPayload["content"]; got != "A moderation action requires private confirmation from the requester." {
		t.Fatalf("unexpected public notice content: %#v", got)
	}
	if _, ok := originalPayload["embeds"]; ok {
		t.Fatalf("did not expect embeds in public notice, got %#v", originalPayload["embeds"])
	}
	if componentsRaw, ok := originalPayload["components"].([]any); !ok || len(componentsRaw) != 0 {
		t.Fatalf("expected public notice to clear components, got %#v", originalPayload["components"])
	}

	var followupPayload map[string]any
	if err := json.Unmarshal(followup.Body, &followupPayload); err != nil {
		t.Fatalf("unmarshal followup payload: %v", err)
	}
	if flags, ok := followupPayload["flags"].(float64); !ok || int(flags) != int(discordgo.MessageFlagsEphemeral) {
		t.Fatalf("expected private followup confirmation, got %#v", followupPayload["flags"])
	}
	if embedsRaw, ok := followupPayload["embeds"].([]any); !ok || len(embedsRaw) != 1 {
		t.Fatalf("expected exactly one embed in followup confirmation, got %#v", followupPayload["embeds"])
	}
	if componentsRaw, ok := followupPayload["components"].([]any); !ok || len(componentsRaw) == 0 {
		t.Fatalf("expected components in followup confirmation, got %#v", followupPayload["components"])
	}

	rawAudit := auditBuf.String()
	if !strings.Contains(rawAudit, "\"action_type\":\"tool_call_proposed\"") {
		t.Fatalf("expected tool_call_proposed audit event, got %q", rawAudit)
	}
}

func TestAskExecuteToolCallFollowupFailureSanitizesPublicError(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	restoreManager := stubAIManager(t, &capturingAskProvider{
		result: &ai.AskResult{
			ToolCalls: []*ai.ToolCall{
				{
					Tool:   "kick",
					User:   "Target User",
					Reason: "spam",
				},
			},
		},
	})
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	recorder := &discordRequestRecorder{}
	const rawDiscordMessage = "private upstream trace raw-secret-123"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)

		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/guilds/"+testAskGuildID+"/members/search"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"original","content":"","flags":0}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"` + rawDiscordMessage + `","code":0}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskOwnerID, Username: "owner"},
			Roles: []string{testAskRoleID},
		},
		{
			User:  &discordgo.User{ID: testAskTargetID, Username: "targetuser", GlobalName: "Target User"},
			Nick:  "Target User",
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskOwnerID, []string{testAskRoleID}, "Kick Target User"))

	lifecycle.Wait()

	requests := recorder.snapshot()
	patches := findDiscordRequests(requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")
	if got, want := len(patches), 2; got != want {
		t.Fatalf("expected %d edits to @original, got %d", want, got)
	}

	var noticePayload map[string]any
	if err := json.Unmarshal(patches[0].Body, &noticePayload); err != nil {
		t.Fatalf("unmarshal public notice payload: %v", err)
	}
	if got := noticePayload["content"]; got != "A moderation action requires private confirmation from the requester." {
		t.Fatalf("unexpected public notice content: %#v", got)
	}

	var errorPayload map[string]any
	if err := json.Unmarshal(patches[1].Body, &errorPayload); err != nil {
		t.Fatalf("unmarshal sanitized error payload: %v", err)
	}
	if content, ok := errorPayload["content"]; ok && content != nil {
		t.Fatalf("expected sanitized public error to omit content, got %#v", content)
	}

	embedsRaw, ok := errorPayload["embeds"].([]any)
	if !ok || len(embedsRaw) != 1 {
		t.Fatalf("expected exactly one embed in sanitized public error, got %#v", errorPayload["embeds"])
	}
	embedRaw, ok := embedsRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected embed object, got %#v", embedsRaw[0])
	}
	if got, want := embedRaw["title"], "❌ AI Action Failed"; got != want {
		t.Fatalf("unexpected sanitized error title: got %#v want %#v", got, want)
	}
	if got, want := embedRaw["description"], ai.UserFacingToolExecutionError(errors.New(rawDiscordMessage)); got != want {
		t.Fatalf("unexpected sanitized error description: got %#v want %#v", got, want)
	}
	if desc, _ := embedRaw["description"].(string); strings.Contains(desc, rawDiscordMessage) {
		t.Fatalf("public error leaked raw Discord message: %q", desc)
	}

	followups := findDiscordRequests(requests, http.MethodPost, "/webhooks/"+testAskAppID+"/"+testAskToken)
	if got, want := len(followups), 3; got != want {
		t.Fatalf("expected %d followup retries, got %d", want, got)
	}
}

func stubAskLimiterForTest() func() {
	prev := defaultAskLimiter
	defaultAskLimiter = newAskLimiter(askLimitsConfig{}, time.Now)
	return func() {
		defaultAskLimiter = prev
	}
}

func seedAskGuildState(t *testing.T, session *discordgo.Session, members []*discordgo.Member) {
	t.Helper()

	guild := &discordgo.Guild{
		ID:      testAskGuildID,
		OwnerID: testAskOwnerID,
		Roles: []*discordgo.Role{
			{
				ID:          testAskRoleID,
				Name:        "member",
				Permissions: discordgo.PermissionViewChannel,
			},
		},
		Members: members,
	}
	if err := session.State.GuildAdd(guild); err != nil {
		t.Fatalf("seed guild state: %v", err)
	}
}

func newAskInteractionForUser(userID string, roles []string, question string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			ID:        testAskInteractionID,
			AppID:     testAskAppID,
			Type:      discordgo.InteractionApplicationCommand,
			GuildID:   testAskGuildID,
			ChannelID: testAskChannelID,
			Token:     testAskToken,
			Member: &discordgo.Member{
				User:  &discordgo.User{ID: userID},
				Roles: append([]string(nil), roles...),
			},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "ask",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "question",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: question,
					},
				},
			},
		},
	}
}

type discordRequestRecorder struct {
	mu       sync.Mutex
	requests []capturedDiscordRequest
}

type capturedDiscordRequest struct {
	Method string
	Path   string
	Body   []byte
}

func (r *discordRequestRecorder) add(t *testing.T, req *http.Request) {
	t.Helper()

	body, err := readRequestBody(req)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, capturedDiscordRequest{
		Method: req.Method,
		Path:   req.URL.Path,
		Body:   body,
	})
}

func (r *discordRequestRecorder) snapshot() []capturedDiscordRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]capturedDiscordRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

func (r *discordRequestRecorder) waitForCount(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := len(r.snapshot()); got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %d Discord requests; got %d", want, len(r.snapshot()))
}

func findDiscordRequest(t *testing.T, requests []capturedDiscordRequest, method, pathSuffix string) capturedDiscordRequest {
	t.Helper()

	for _, req := range requests {
		if req.Method == method && strings.HasSuffix(req.Path, pathSuffix) {
			return req
		}
	}

	t.Fatalf("request %s %s not found in %#v", method, pathSuffix, requests)
	return capturedDiscordRequest{}
}

func findDiscordRequests(requests []capturedDiscordRequest, method, pathSuffix string) []capturedDiscordRequest {
	var out []capturedDiscordRequest
	for _, req := range requests {
		if req.Method == method && strings.HasSuffix(req.Path, pathSuffix) {
			out = append(out, req)
		}
	}
	return out
}

func hasDiscordRequest(requests []capturedDiscordRequest, method, pathSuffix string) bool {
	for _, req := range requests {
		if req.Method == method && strings.HasSuffix(req.Path, pathSuffix) {
			return true
		}
	}
	return false
}

func readRequestBody(req *http.Request) ([]byte, error) {
	defer req.Body.Close()
	return io.ReadAll(req.Body)
}

func TestAskExecuteLongResponseTruncation(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	// 2500 character long answer
	longAnswer := strings.Repeat("A", 2500)

	restoreManager := stubAIManager(t, &capturingAskProvider{
		result: &ai.AskResult{Text: longAnswer},
	})
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"100000000000000009","channel_id":"` + testAskChannelID + `","content":"ok"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskUserID, Username: "requester"},
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskUserID, []string{testAskRoleID}, "Tell me a long story."))

	recorder.waitForCount(t, 2)
	lifecycle.Wait()

	requests := recorder.snapshot()
	editOriginal := findDiscordRequest(t, requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")

	var payload map[string]any
	if err := json.Unmarshal(editOriginal.Body, &payload); err != nil {
		t.Fatalf("unmarshal original edit payload: %v", err)
	}
	content := payload["content"].(string)
	if len(content) != 2000 {
		t.Fatalf("expected content length to be truncated to exactly 2000, got %d", len(content))
	}
	if strings.Contains(content, "truncated") {
		t.Fatalf("content should not contain the truncation suffix anymore")
	}
}

func TestAskExecuteLongResponseMultiMessageSplitting(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}
	if err := d.SetGuildMultiMessage(testAskGuildID, true); err != nil {
		t.Fatalf("set multi message: %v", err)
	}

	// 4500 character long answer
	longAnswer := strings.Repeat("A", 2000) + strings.Repeat("B", 2000) + strings.Repeat("C", 500)

	restoreManager := stubAIManager(t, &capturingAskProvider{
		result: &ai.AskResult{Text: longAnswer},
	})
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"100000000000000009","channel_id":"` + testAskChannelID + `","content":"ok"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"123","content":"ok"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskUserID, Username: "requester"},
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskUserID, []string{testAskRoleID}, "Tell me a long story."))

	// Expect 4 requests: 1 callback, 1 patch original, 2 followup posts
	recorder.waitForCount(t, 4)
	lifecycle.Wait()

	requests := recorder.snapshot()
	editOriginal := findDiscordRequest(t, requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")
	followups := findDiscordRequests(requests, http.MethodPost, "/webhooks/"+testAskAppID+"/"+testAskToken)

	var payload map[string]any
	if err := json.Unmarshal(editOriginal.Body, &payload); err != nil {
		t.Fatalf("unmarshal original edit payload: %v", err)
	}
	content1 := payload["content"].(string)
	if content1 != strings.Repeat("A", 2000) {
		t.Fatalf("first chunk does not match expected output")
	}

	if len(followups) != 2 {
		t.Fatalf("expected 2 followups, got %d", len(followups))
	}

	var payload2 map[string]any
	if err := json.Unmarshal(followups[0].Body, &payload2); err != nil {
		t.Fatalf("unmarshal followup 1: %v", err)
	}
	content2 := payload2["content"].(string)
	if content2 != strings.Repeat("B", 2000) {
		t.Fatalf("second chunk does not match expected output")
	}

	var payload3 map[string]any
	if err := json.Unmarshal(followups[1].Body, &payload3); err != nil {
		t.Fatalf("unmarshal followup 2: %v", err)
	}
	content3 := payload3["content"].(string)
	if content3 != strings.Repeat("C", 500) {
		t.Fatalf("third chunk does not match expected output")
	}
}

func TestAskExecuteIncludesChannelHistoryContext(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	capture := &capturingAskProvider{
		result: &ai.AskResult{Text: "Response text"},
	}
	restoreManager := stubAIManager(t, capture)
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)

		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"id":"msg-2","channel_id":"` + testAskChannelID + `","content":"msg 2 content","author":{"id":"user-2","username":"user2"}},
				{"id":"msg-1","channel_id":"` + testAskChannelID + `","content":"msg 1 content","author":{"id":"user-1","username":"user1"}}
			]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"original-id","content":"ok"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskUserID, Username: "requester"},
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskUserID, []string{testAskRoleID}, "Is the sky blue?"))

	// Expect 3 requests: callback, get messages, patch original
	recorder.waitForCount(t, 3)
	lifecycle.Wait()

	requests := recorder.snapshot()
	_ = findDiscordRequest(t, requests, http.MethodPost, "/interactions/"+testAskInteractionID+"/"+testAskToken+"/callback")
	_ = findDiscordRequest(t, requests, http.MethodGet, "/channels/"+testAskChannelID+"/messages")
	_ = findDiscordRequest(t, requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")

	// Verify the prompt passed to the provider
	prompt := capture.receivedPrompt
	if !strings.Contains(prompt.BaseSystem, "CHANNEL_HISTORY_BLOCK_") {
		t.Errorf("expected BaseSystem to contain channel history instructions, got:\n%s", prompt.BaseSystem)
	}
	if !strings.Contains(prompt.BaseSystem, "untrusted reference history") {
		t.Errorf("expected BaseSystem to contain untrusted reference instructions, got:\n%s", prompt.BaseSystem)
	}

	if !strings.Contains(prompt.UserPrompt, "CHANNEL_HISTORY_BLOCK_") {
		t.Errorf("expected UserPrompt to contain boundary delimiters, got:\n%s", prompt.UserPrompt)
	}
	if !strings.Contains(prompt.UserPrompt, "user1: msg 1 content") {
		t.Errorf("expected UserPrompt to contain chronological history context from user1, got:\n%s", prompt.UserPrompt)
	}
	if !strings.Contains(prompt.UserPrompt, "user2: msg 2 content") {
		t.Errorf("expected UserPrompt to contain chronological history context from user2, got:\n%s", prompt.UserPrompt)
	}
	if !strings.HasSuffix(prompt.UserPrompt, "Question: Is the sky blue?") {
		t.Errorf("expected UserPrompt to end with the question, got:\n%s", prompt.UserPrompt)
	}
}

func TestAskExecuteMultiToolCallUsesConsolidatedNotice(t *testing.T) {
	d := newSlashTestDatabase(t)
	if err := d.SetGuildConfig(testAskGuildID, "secret-api-key", "Capture", "model-1"); err != nil {
		t.Fatalf("set guild config: %v", err)
	}

	restoreManager := stubAIManager(t, &capturingAskProvider{
		result: &ai.AskResult{
			ToolCalls: []*ai.ToolCall{
				{
					Tool:   "kick",
					User:   "UserA",
					Reason: "spam",
				},
				{
					Tool:   "kick",
					User:   "UserB",
					Reason: "flood",
				},
			},
		},
	})
	defer restoreManager()

	restoreLimiter := stubAskLimiterForTest()
	defer restoreLimiter()

	var auditBuf bytes.Buffer
	logger := audit.New(&auditBuf, nil)
	restoreAudit := audit.SetDefaultForTest(logger)
	defer restoreAudit()

	recorder := &discordRequestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(t, r)

		switch {
		case strings.HasSuffix(r.URL.Path, "/callback"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/"+testAskChannelID+"/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/guilds/"+testAskGuildID+"/members/search"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"original","content":"","flags":64}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/webhooks/"+testAskAppID+"/"+testAskToken):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"followup-1","content":"","flags":64}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	restoreEndpoints := stubDiscordEndpoints(t, server.URL+"/")
	defer restoreEndpoints()

	lifecycle.Init()
	defer func() {
		lifecycle.Cancel()
		lifecycle.Wait()
	}()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	session.Client = server.Client()
	session.SyncEvents = true

	seedAskGuildState(t, session, []*discordgo.Member{
		{
			User:  &discordgo.User{ID: testAskOwnerID, Username: "owner"},
			Roles: []string{testAskRoleID},
		},
		{
			User:  &discordgo.User{ID: "100000000000000007", Username: "usera", GlobalName: "UserA"},
			Nick:  "UserA",
			Roles: []string{testAskRoleID},
		},
		{
			User:  &discordgo.User{ID: "100000000000000008", Username: "userb", GlobalName: "UserB"},
			Nick:  "UserB",
			Roles: []string{testAskRoleID},
		},
	})

	cmd := registeredSlashCommand(t, "ask")
	cmd.Execute(session, newAskInteractionForUser(testAskOwnerID, []string{testAskRoleID}, "Kick UserA and UserB"))

	recorder.waitForCount(t, 3)

	lifecycle.Cancel()
	lifecycle.Wait()

	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("close audit logger: %v", err)
	}

	requests := recorder.snapshot()
	findDiscordRequest(t, requests, http.MethodPost, "/interactions/"+testAskInteractionID+"/"+testAskToken+"/callback")
	editOriginal := findDiscordRequest(t, requests, http.MethodPatch, "/webhooks/"+testAskAppID+"/"+testAskToken+"/messages/@original")

	var originalPayload map[string]any
	if err := json.Unmarshal(editOriginal.Body, &originalPayload); err != nil {
		t.Fatalf("unmarshal original edit payload: %v", err)
	}
	if got := originalPayload["content"]; got != "Moderation actions require private confirmation from the requester." {
		t.Fatalf("unexpected public notice content: %#v", got)
	}
}
