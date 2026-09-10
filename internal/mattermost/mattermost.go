// Package mattermost posts to the house's chat as the registry bot.
//
// The registry only ever writes: it announces what happened to a channel the
// relevant group already reads. It never reads the chat, looks anybody up, or
// treats a message as an instruction — which is what keeps this a notifier
// rather than a second way into the register.
//
// When no server or token is configured the client is disabled and messages
// are written to the log instead of being sent. That keeps local development
// and the demo working without a chat server, and it means a house that has
// no Mattermost loses nothing but the announcements.
package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// timeout bounds a single API call. The bot is on the same network as the
// site, so anything slower than this is broken rather than busy.
const timeout = 15 * time.Second

// User is the part of a Mattermost account the registry cares about, which is
// almost nothing: it identifies the bot itself, and the person a development
// test message is addressed to.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	IsBot     bool   `json:"is_bot"`
	DeleteAt  int64  `json:"delete_at"`
}

// DisplayName is the name to show: real name when the account has one,
// otherwise the username.
func (u User) DisplayName() string {
	if full := strings.TrimSpace(u.FirstName + " " + u.LastName); full != "" {
		return full
	}
	return u.Username
}

// Client is the bot's connection to Mattermost.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	log     *slog.Logger

	// self is the bot's own account, needed to open a direct channel. It is
	// filled by Verify at startup.
	self User
}

// New builds a client. An empty url or token yields a disabled client.
func New(baseURL, token string, log *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: timeout},
		log:     log,
	}
}

// Enabled reports whether calls will really reach a Mattermost server.
func (c *Client) Enabled() bool { return c.baseURL != "" && c.token != "" }

// BaseURL is the server address, for links back into Mattermost.
func (c *Client) BaseURL() string { return c.baseURL }

// Bot returns the bot's own account, once Verify has run.
func (c *Client) Bot() User { return c.self }

// Verify checks the token and remembers who the bot is. Callers should run it
// at startup so a bad token is a loud failure at boot rather than a mystery
// the first time somebody registers an interest.
func (c *Client) Verify(ctx context.Context) (User, error) {
	if !c.Enabled() {
		return User{}, nil
	}
	var u User
	if err := c.call(ctx, http.MethodGet, "/api/v4/users/me", nil, &u); err != nil {
		return User{}, err
	}
	c.self = u
	return u, nil
}

// Post writes one message to a channel.
//
// The channel is given by id rather than by name. A name can be changed by
// anybody in the channel, and an announcement that quietly stops arriving
// because somebody renamed a channel is worse than one that fails loudly.
func (c *Client) Post(ctx context.Context, channelID, message string) error {
	if !c.Enabled() {
		c.log.Warn("mattermost not configured, message not sent", "channel", channelID)
		c.log.Debug("mattermost message body", "channel", channelID, "message", message)
		return nil
	}
	if strings.TrimSpace(channelID) == "" {
		return fmt.Errorf("no mattermost channel to post to")
	}
	return c.call(ctx, http.MethodPost, "/api/v4/posts",
		map[string]any{"channel_id": channelID, "message": message}, nil)
}

// DM sends a direct message from the bot to one person. It exists for
// development: while the messages are being written, they go to whoever is
// writing them rather than to a channel the whole house reads.
func (c *Client) DM(ctx context.Context, userID, message string) error {
	if !c.Enabled() {
		c.log.Warn("mattermost not configured, direct message not sent", "user", userID)
		c.log.Debug("mattermost message body", "user", userID, "message", message)
		return nil
	}
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("no mattermost user id to send to")
	}
	if c.self.ID == "" {
		if _, err := c.Verify(ctx); err != nil {
			return fmt.Errorf("identify bot: %w", err)
		}
	}

	// A direct channel is created on demand and reused afterwards, so asking
	// for it every time is both correct and cheap.
	var channel struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v4/channels/direct",
		[]string{c.self.ID, userID}, &channel); err != nil {
		return fmt.Errorf("open direct channel: %w", err)
	}
	return c.Post(ctx, channel.ID, message)
}

// call performs one API request, encoding body and decoding into out when
// they are non-nil.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mattermost %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		// Drain so the connection can be reused.
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// apiError turns Mattermost's error body into something readable in a log.
func apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Message string `json:"message"`
		ID      string `json:"id"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return fmt.Errorf("mattermost %s: %s", resp.Status, e.Message)
	}
	return fmt.Errorf("mattermost %s", resp.Status)
}
