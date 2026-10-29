package google

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

type EventTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

type Person struct {
	Email string `json:"email,omitempty"`
	Self  bool   `json:"self,omitempty"`
}

// Event — событие календаря. Участников и описания сервис не пишет никогда:
// поля Attendees здесь только для чтения.
type Event struct {
	ID        string     `json:"id,omitempty"`
	Status    string     `json:"status,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Location  string     `json:"location,omitempty"`
	Start     *EventTime `json:"start,omitempty"`
	End       *EventTime `json:"end,omitempty"`
	Attendees []Person   `json:"attendees,omitempty"`
	Organizer *Person    `json:"organizer,omitempty"`
	Recurring string     `json:"recurringEventId,omitempty"`
}

// Own — событие владельца: он организатор и других участников нет. Только
// такие сервис переносит и удаляет — чужие приглашения не трогает.
func (e Event) Own() bool {
	if e.Organizer != nil && !e.Organizer.Self {
		return false
	}
	for _, a := range e.Attendees {
		if !a.Self {
			return false
		}
	}
	return true
}

// Times — начало и конец; allDay — событие на весь день (конец — следующий
// день, как у Google).
func (e Event) Times(loc *time.Location) (start, end time.Time, allDay bool, err error) {
	if e.Start == nil || e.End == nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("событие без времени")
	}
	if e.Start.Date != "" {
		start, err = time.ParseInLocation("2006-01-02", e.Start.Date, loc)
		if err == nil {
			end, err = time.ParseInLocation("2006-01-02", e.End.Date, loc)
		}
		return start, end, true, err
	}
	start, err = time.Parse(time.RFC3339, e.Start.DateTime)
	if err == nil {
		end, err = time.Parse(time.RFC3339, e.End.DateTime)
	}
	return start.In(loc), end.In(loc), false, err
}

// Events — события основного календаря в окне, повторяющиеся — развёрнутыми.
func (c *Client) Events(ctx context.Context, from, to time.Time) ([]Event, error) {
	q := url.Values{
		"timeMin":      {from.Format(time.RFC3339)},
		"timeMax":      {to.Format(time.RFC3339)},
		"singleEvents": {"true"},
		"orderBy":      {"startTime"},
		"maxResults":   {"250"},
	}
	var resp struct {
		Items []Event `json:"items"`
	}
	if err := c.do(ctx, "GET", c.calendarBase+"/events?"+q.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range resp.Items {
		if e.Status != "cancelled" {
			out = append(out, e)
		}
	}
	return out, nil
}

func (c *Client) Event(ctx context.Context, id string) (*Event, error) {
	var e Event
	if err := c.do(ctx, "GET", c.calendarBase+"/events/"+url.PathEscape(id), nil, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// noInvites — приглашений никому не рассылаем, даже если у события
// оказались участники.
const noInvites = "?sendUpdates=none"

func (c *Client) Insert(ctx context.Context, e Event) (*Event, error) {
	e.ID, e.Attendees, e.Organizer = "", nil, nil
	var out Event
	if err := c.do(ctx, "POST", c.calendarBase+"/events"+noInvites, e, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Move меняет только время события.
func (c *Client) Move(ctx context.Context, id string, start, end EventTime) (*Event, error) {
	var out Event
	body := map[string]EventTime{"start": start, "end": end}
	if err := c.do(ctx, "PATCH", c.calendarBase+"/events/"+url.PathEscape(id)+noInvites, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", c.calendarBase+"/events/"+url.PathEscape(id)+noInvites, nil, nil)
}
