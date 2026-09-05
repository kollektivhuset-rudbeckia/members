package google

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

const sheetsBase = "https://sheets.googleapis.com/v4/spreadsheets"

// Grid is a rectangle of cell values, rows first.
type Grid [][]any

// SheetInfo is what the registry needs to know about a tab before writing it.
type SheetInfo struct {
	Title   string
	SheetID int
	Rows    int
	Columns int
}

// Sheet finds a tab by name.
func (c *Client) Sheet(ctx context.Context, mailbox, spreadsheetID, tab string) (SheetInfo, error) {
	endpoint := fmt.Sprintf("%s/%s?fields=%s", sheetsBase, url.PathEscape(spreadsheetID),
		url.QueryEscape("sheets.properties(sheetId,title,gridProperties)"))
	var out struct {
		Sheets []struct {
			Properties struct {
				SheetID   int    `json:"sheetId"`
				Title     string `json:"title"`
				GridProps struct {
					RowCount    int `json:"rowCount"`
					ColumnCount int `json:"columnCount"`
				} `json:"gridProperties"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	if err := c.call(ctx, mailbox, SheetsScopes, "GET", endpoint, nil, &out); err != nil {
		// The registry opens the spreadsheet *as* mailbox, not as itself, so
		// that is the address the sheet has to be shared with. Naming the
		// service account here — as this used to — sends somebody to share a
		// document with an address that will never open it.
		if NotFound(err) {
			return SheetInfo{}, fmt.Errorf("there is no spreadsheet with id %s that %s can see "+
				"— share it with %s as an editor, or correct sheet.id in config.yaml",
				spreadsheetID, mailbox, mailbox)
		}
		if Forbidden(err) {
			return SheetInfo{}, fmt.Errorf("%s may not open the spreadsheet: share it with "+
				"%s as an editor (the registry writes the sheet as that account, not as "+
				"the service account %s)", mailbox, mailbox, c.Account())
		}
		return SheetInfo{}, err
	}
	var titles []string
	for _, s := range out.Sheets {
		titles = append(titles, s.Properties.Title)
		if strings.EqualFold(s.Properties.Title, tab) {
			return SheetInfo{
				Title:   s.Properties.Title,
				SheetID: s.Properties.SheetID,
				Rows:    s.Properties.GridProps.RowCount,
				Columns: s.Properties.GridProps.ColumnCount,
			}, nil
		}
	}
	return SheetInfo{}, fmt.Errorf("the spreadsheet has no tab called %q (it has %s); "+
		"add the tab, or correct sheet.tab in config.yaml", tab, strings.Join(titles, ", "))
}

// WriteSheet replaces the whole of a tab with a grid.
//
// The tab is cleared first and then written, rather than written over, so
// that a register which has shrunk does not leave last month's leavers
// sitting below the new last row. That is two calls where one would nearly
// do, and the "nearly" is the reason: a stale row in a spreadsheet the
// cashiers do arithmetic on is worse than a slow sync.
func (c *Client) WriteSheet(ctx context.Context, mailbox, spreadsheetID, tab string, grid Grid) error {
	clear := fmt.Sprintf("%s/%s/values/%s:clear",
		sheetsBase, url.PathEscape(spreadsheetID), url.PathEscape(quoteTab(tab)))
	if err := c.call(ctx, mailbox, SheetsScopes, "POST", clear, map[string]any{}, nil); err != nil {
		return fmt.Errorf("clear the tab %q: %w", tab, err)
	}
	if len(grid) == 0 {
		return nil
	}
	// A1 rather than a computed range: the values themselves say how far the
	// grid reaches, and letting Google work the corner out means the column
	// count can change without anybody remembering to update a range string.
	write := fmt.Sprintf("%s/%s/values/%s?valueInputOption=RAW",
		sheetsBase, url.PathEscape(spreadsheetID), url.PathEscape(quoteTab(tab)+"!A1"))
	body := map[string]any{"values": grid}
	if err := c.call(ctx, mailbox, SheetsScopes, "PUT", write, body, nil); err != nil {
		return fmt.Errorf("write the tab %q: %w", tab, err)
	}
	return nil
}

// FreezeHeader pins the first row, so the cashiers can scroll a long register
// and still see which column is which. It is cosmetic, and a failure to do it
// is not worth failing a synchronisation over.
func (c *Client) FreezeHeader(ctx context.Context, mailbox, spreadsheetID string, sheetID int) error {
	endpoint := fmt.Sprintf("%s/%s:batchUpdate", sheetsBase, url.PathEscape(spreadsheetID))
	body := map[string]any{"requests": []any{
		map[string]any{"updateSheetProperties": map[string]any{
			"properties": map[string]any{
				"sheetId":        sheetID,
				"gridProperties": map[string]any{"frozenRowCount": 1},
			},
			"fields": "gridProperties.frozenRowCount",
		}},
	}}
	return c.call(ctx, mailbox, SheetsScopes, "POST", endpoint, body, nil)
}

// quoteTab wraps a tab name in the single quotes A1 notation wants whenever
// the name has a space in it — "Medlemmar 2026!A1" is not the same range as
// "'Medlemmar 2026'!A1", and the unquoted one is an error.
func quoteTab(tab string) string {
	if !strings.ContainsAny(tab, " '") {
		return tab
	}
	return "'" + strings.ReplaceAll(tab, "'", "''") + "'"
}
