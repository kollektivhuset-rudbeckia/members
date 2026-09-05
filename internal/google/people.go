package google

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const peopleBase = "https://people.googleapis.com/v1"

// personFields is what the registry reads back about a contact. Asking for
// less than this makes updateContact drop whatever it was not told about, so
// the list here and the one in Contact.updateMask below have to agree.
const personFields = "names,emailAddresses,phoneNumbers,organizations,biographies,memberships,metadata"

// Person is the part of a People API contact the registry has an opinion
// about. Everything else on the card — a birthday, a photograph, whatever the
// owner of the address book has added themselves — is left strictly alone.
type Person struct {
	ResourceName string       `json:"resourceName,omitempty"`
	ETag         string       `json:"etag,omitempty"`
	Names        []Name       `json:"names,omitempty"`
	Emails       []Email      `json:"emailAddresses,omitempty"`
	Phones       []Phone      `json:"phoneNumbers,omitempty"`
	Biographies  []Biography  `json:"biographies,omitempty"`
	Memberships  []Membership `json:"memberships,omitempty"`
	Metadata     *PersonMeta  `json:"metadata,omitempty"`
}

// Name is a contact's name. Google keeps the parts and the whole; the
// registry writes all three so the card looks right in every client.
type Name struct {
	DisplayName string `json:"displayName,omitempty"`
	GivenName   string `json:"givenName,omitempty"`
	FamilyName  string `json:"familyName,omitempty"`
}

// Email is one address on a contact card.
type Email struct {
	Value string `json:"value,omitempty"`
	Type  string `json:"type,omitempty"`
}

// Phone is one telephone number.
type Phone struct {
	Value string `json:"value,omitempty"`
	Type  string `json:"type,omitempty"`
}

// Biography is the notes field. The registry writes a single line saying what
// sort of member this is and when they joined, so that somebody looking the
// person up on their phone gets the answer without opening the register.
type Biography struct {
	Value       string `json:"value,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// Membership ties a contact to a label.
type Membership struct {
	ContactGroupMembership *ContactGroupMembership `json:"contactGroupMembership,omitempty"`
}

// ContactGroupMembership names the label.
type ContactGroupMembership struct {
	ContactGroupID           string `json:"contactGroupId,omitempty"`
	ContactGroupResourceName string `json:"contactGroupResourceName,omitempty"`
}

// PersonMeta carries the deleted flag, which a listing can still return.
type PersonMeta struct {
	Deleted bool `json:"deleted,omitempty"`
}

// PrimaryEmail is the address the registry matches a contact on.
func (p Person) PrimaryEmail() string {
	for _, e := range p.Emails {
		if v := strings.ToLower(strings.TrimSpace(e.Value)); v != "" {
			return v
		}
	}
	return ""
}

// DisplayName is the contact's name as written on the card.
func (p Person) DisplayName() string {
	for _, n := range p.Names {
		if n.DisplayName != "" {
			return n.DisplayName
		}
	}
	return ""
}

// PrimaryPhone is the first telephone number on the card.
func (p Person) PrimaryPhone() string {
	for _, ph := range p.Phones {
		if v := strings.TrimSpace(ph.Value); v != "" {
			return v
		}
	}
	return ""
}

// Notes is the biography line.
func (p Person) Notes() string {
	for _, b := range p.Biographies {
		if b.Value != "" {
			return b.Value
		}
	}
	return ""
}

// ID is the bare "c12345" out of a "people/c12345" resource name.
func (p Person) ID() string { return strings.TrimPrefix(p.ResourceName, "people/") }

// ContactGroup is a label in somebody's address book.
type ContactGroup struct {
	ResourceName string `json:"resourceName,omitempty"`
	Name         string `json:"name,omitempty"`
	GroupType    string `json:"groupType,omitempty"`
	// MemberResourceNames is only returned by a get that asked for members.
	MemberResourceNames []string `json:"memberResourceNames,omitempty"`
	MemberCount         int      `json:"memberCount,omitempty"`
}

// ID is the bare id out of a "contactGroups/abc" resource name.
func (g ContactGroup) ID() string { return strings.TrimPrefix(g.ResourceName, "contactGroups/") }

// EnsureLabel finds the label by name in the mailbox's address book, creating
// it if it is not there. Creating it is the right default: an empty address
// book on a fresh mailbox should not need a human to click "new label" before
// the registry can do its job.
func (c *Client) EnsureLabel(ctx context.Context, mailbox, name string) (ContactGroup, error) {
	groups, err := c.contactGroups(ctx, mailbox)
	if err != nil {
		return ContactGroup{}, err
	}
	// An exact name wins. Failing that, a label whose name merely starts with
	// what we asked for is taken: the association's existing labels are found
	// that way by the Apps Script this replaces, and a label called
	// "Bomedlemmar 2026" or "Bomedlemmar (aktuella)" is plainly the one meant.
	// Creating a second, identically-prefixed label beside it would split the
	// address book in two and lose half the members.
	var prefixed *ContactGroup
	for i, g := range groups {
		// Only our own labels are candidates. Google's built-in groups
		// ("myContacts", "starred") are not USER_CONTACT_GROUP and must never
		// be written to.
		if g.GroupType != "USER_CONTACT_GROUP" {
			continue
		}
		if strings.EqualFold(g.Name, name) {
			return g, nil
		}
		if prefixed == nil && strings.HasPrefix(strings.ToLower(g.Name), strings.ToLower(name)) {
			prefixed = &groups[i]
		}
	}
	if prefixed != nil {
		return *prefixed, nil
	}
	var created struct {
		ContactGroup
	}
	body := map[string]any{"contactGroup": map[string]string{"name": name}}
	if err := c.call(ctx, mailbox, ContactsScopes, "POST",
		peopleBase+"/contactGroups", body, &created); err != nil {
		return ContactGroup{}, fmt.Errorf("create the label %q in %s: %w", name, mailbox, err)
	}
	return created.ContactGroup, nil
}

func (c *Client) contactGroups(ctx context.Context, mailbox string) ([]ContactGroup, error) {
	var out []ContactGroup
	pageToken := ""
	for {
		q := url.Values{"pageSize": {"200"}, "groupFields": {"name,groupType,memberCount"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page struct {
			ContactGroups []ContactGroup `json:"contactGroups"`
			NextPageToken string         `json:"nextPageToken"`
		}
		if err := c.call(ctx, mailbox, ContactsScopes, "GET",
			peopleBase+"/contactGroups?"+q.Encode(), nil, &page); err != nil {
			if Forbidden(err) {
				return nil, fmt.Errorf("the service account %s may not read %s's contacts: "+
					"grant it %s under Domain-wide delegation", c.Account(), mailbox, ScopeContacts)
			}
			return nil, fmt.Errorf("read the labels in %s: %w", mailbox, err)
		}
		out = append(out, page.ContactGroups...)
		if page.NextPageToken == "" {
			return out, nil
		}
		pageToken = page.NextPageToken
	}
}

// LabelMembers returns the contacts carrying a label.
//
// Google gives the label's members as bare resource names and will only hand
// over a thousand of them, which is several times the whole association and
// so is treated as a hard ceiling rather than paged: if a label ever holds
// more than that, something has gone wrong that quietly reading the first
// thousand would hide.
func (c *Client) LabelMembers(ctx context.Context, mailbox, groupResource string) ([]Person, error) {
	var group ContactGroup
	endpoint := fmt.Sprintf("%s/%s?maxMembers=1000&groupFields=name,groupType,memberCount",
		peopleBase, groupResource)
	if err := c.call(ctx, mailbox, ContactsScopes, "GET", endpoint, nil, &group); err != nil {
		return nil, fmt.Errorf("read the label %s in %s: %w", groupResource, mailbox, err)
	}
	if group.MemberCount > 1000 {
		return nil, fmt.Errorf("the label %q in %s holds %d contacts, more than the 1000 "+
			"Google will list; sort that out by hand before the registry touches it",
			group.Name, mailbox, group.MemberCount)
	}
	return c.batchGet(ctx, mailbox, group.MemberResourceNames)
}

// batchGet reads contacts by resource name, in the chunks Google allows.
func (c *Client) batchGet(ctx context.Context, mailbox string, names []string) ([]Person, error) {
	const chunk = 200 // people:batchGet refuses more than 200 at a time
	var out []Person
	for start := 0; start < len(names); start += chunk {
		end := start + chunk
		if end > len(names) {
			end = len(names)
		}
		q := url.Values{"personFields": {personFields}}
		for _, name := range names[start:end] {
			q.Add("resourceNames", name)
		}
		var page struct {
			Responses []struct {
				Person     Person `json:"person"`
				HTTPStatus int    `json:"httpStatusCode"`
			} `json:"responses"`
		}
		if err := c.call(ctx, mailbox, ContactsScopes, "GET",
			peopleBase+"/people:batchGet?"+q.Encode(), nil, &page); err != nil {
			return nil, fmt.Errorf("read contacts in %s: %w", mailbox, err)
		}
		for _, r := range page.Responses {
			// A contact deleted between the label listing and this read comes
			// back empty. Skipping it is right: it is already gone.
			if r.Person.ResourceName == "" || (r.Person.Metadata != nil && r.Person.Metadata.Deleted) {
				continue
			}
			out = append(out, r.Person)
		}
	}
	return out, nil
}

// CreateContact writes a new card and puts it in the label.
//
// The label membership is a second call rather than part of the card: the
// People API accepts memberships on a create, but has been inconsistent about
// honouring them, and a contact that exists outside every label is invisible
// to the next run and would be created again on each pass.
func (c *Client) CreateContact(ctx context.Context, mailbox, groupResource string, p Person) (Person, error) {
	var created Person
	endpoint := peopleBase + "/people:createContact?personFields=" + url.QueryEscape(personFields)
	if err := c.call(ctx, mailbox, ContactsScopes, "POST", endpoint, p, &created); err != nil {
		return created, fmt.Errorf("add %s to %s's contacts: %w", p.PrimaryEmail(), mailbox, err)
	}
	if err := c.ModifyLabel(ctx, mailbox, groupResource, []string{created.ResourceName}, nil); err != nil {
		return created, err
	}
	return created, nil
}

// UpdateContact rewrites the fields the registry owns on an existing card.
//
// The mask is deliberately short. Everything the owner of the address book
// added themselves stays where it is: the registry is a source for a name, an
// address, a number and a one-line note, and nothing else on the card is any
// of its business.
func (c *Client) UpdateContact(ctx context.Context, mailbox string, p Person) error {
	if p.ETag == "" {
		return fmt.Errorf("refusing to update %s in %s without an etag; "+
			"Google would overwrite whatever changed in between",
			p.PrimaryEmail(), mailbox)
	}
	endpoint := fmt.Sprintf("%s/%s:updateContact?updatePersonFields=%s&personFields=%s",
		peopleBase, p.ResourceName,
		url.QueryEscape("names,emailAddresses,phoneNumbers,biographies"),
		url.QueryEscape(personFields))
	if err := c.call(ctx, mailbox, ContactsScopes, "PATCH", endpoint, p, nil); err != nil {
		return fmt.Errorf("update %s in %s's contacts: %w", p.PrimaryEmail(), mailbox, err)
	}
	return nil
}

// DeleteContact removes a card outright.
func (c *Client) DeleteContact(ctx context.Context, mailbox, resourceName string) error {
	endpoint := fmt.Sprintf("%s/%s:deleteContact", peopleBase, resourceName)
	err := c.call(ctx, mailbox, ContactsScopes, "DELETE", endpoint, nil, nil)
	if NotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove a contact from %s: %w", mailbox, err)
	}
	return nil
}

// ModifyLabel adds and removes contacts from a label without touching the
// cards themselves. It is how somebody who moves from vänmedlem to bomedlem
// changes labels while keeping the same card, notes and history.
func (c *Client) ModifyLabel(ctx context.Context, mailbox, groupResource string, add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	endpoint := fmt.Sprintf("%s/%s/members:modify", peopleBase, groupResource)
	body := map[string]any{}
	if len(add) > 0 {
		body["resourceNamesToAdd"] = add
	}
	if len(remove) > 0 {
		body["resourceNamesToRemove"] = remove
	}
	if err := c.call(ctx, mailbox, ContactsScopes, "POST", endpoint, body, nil); err != nil {
		return fmt.Errorf("change the label in %s's contacts: %w", mailbox, err)
	}
	return nil
}
