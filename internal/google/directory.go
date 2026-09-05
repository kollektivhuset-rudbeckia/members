package google

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const directoryBase = "https://admin.googleapis.com/admin/directory/v1"

// GroupMember is one address in a Google group.
type GroupMember struct {
	ID    string `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
	Role  string `json:"role,omitempty"`
	Type  string `json:"type,omitempty"`
	// Status is what Google thinks of the address: ACTIVE, or SUSPENDED for
	// one that has been bouncing. It is shown on the sync page, because an
	// address that is in the group and still not receiving anything is
	// exactly the sort of quiet failure the board asked to be told about.
	Status string `json:"status,omitempty"`
}

// GroupMembers lists everybody in a group, following Google's paging.
//
// admin is the Workspace administrator to act as: managing a group is an
// admin operation, and a service account cannot do it as itself.
func (c *Client) GroupMembers(ctx context.Context, admin, group string) ([]GroupMember, error) {
	var out []GroupMember
	pageToken := ""
	for {
		q := url.Values{"maxResults": {"200"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		endpoint := fmt.Sprintf("%s/groups/%s/members?%s",
			directoryBase, url.PathEscape(group), q.Encode())

		var page struct {
			Members       []GroupMember `json:"members"`
			NextPageToken string        `json:"nextPageToken"`
		}
		if err := c.call(ctx, admin, DirectoryScopes, "GET", endpoint, nil, &page); err != nil {
			if NotFound(err) {
				return nil, fmt.Errorf("there is no group %s in the Workspace", group)
			}
			return nil, err
		}
		out = append(out, page.Members...)
		if page.NextPageToken == "" {
			return out, nil
		}
		pageToken = page.NextPageToken
	}
}

// AddToGroup puts an address in a group. An address that is already there is
// not an error: the registry's job is to make the group match the register,
// and a group that already matches has nothing to complain about.
func (c *Client) AddToGroup(ctx context.Context, admin, group, email, role string) error {
	endpoint := fmt.Sprintf("%s/groups/%s/members", directoryBase, url.PathEscape(group))
	body := GroupMember{Email: email, Role: role, Type: "USER"}
	err := c.call(ctx, admin, DirectoryScopes, "POST", endpoint, body, nil)
	if Conflict(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("add %s to %s: %w", email, group, err)
	}
	return nil
}

// RemoveFromGroup takes an address out of a group. An address that is already
// gone is likewise not an error.
func (c *Client) RemoveFromGroup(ctx context.Context, admin, group, email string) error {
	endpoint := fmt.Sprintf("%s/groups/%s/members/%s",
		directoryBase, url.PathEscape(group), url.PathEscape(email))
	err := c.call(ctx, admin, DirectoryScopes, "DELETE", endpoint, nil, nil)
	if NotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove %s from %s: %w", email, group, err)
	}
	return nil
}

// GroupExists checks that a group is really there, so that a typo in
// config.yaml is reported as a typo at startup rather than as a mysterious
// failure to add every member, ten minutes later.
func (c *Client) GroupExists(ctx context.Context, admin, group string) error {
	endpoint := fmt.Sprintf("%s/groups/%s", directoryBase, url.PathEscape(group))
	var out struct {
		Email string `json:"email"`
	}
	if err := c.call(ctx, admin, DirectoryScopes, "GET", endpoint, nil, &out); err != nil {
		if NotFound(err) {
			return fmt.Errorf("there is no group %s in the Workspace; "+
				"create it, or correct the address in config.yaml", group)
		}
		if Forbidden(err) {
			return fmt.Errorf("the service account %s may not read %s: grant it %s "+
				"under Domain-wide delegation, and check that GOOGLE_ADMIN_SUBJECT (%s) "+
				"is really an administrator",
				c.Account(), group, strings.Join(DirectoryScopes, ", "), admin)
		}
		return err
	}
	return nil
}
