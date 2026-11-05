package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robinvdvleuten/pim-mcp/internal/calendar"
)

const calendarTimeout = time.Minute

// CalendarList is what calendar_list_calendars returns.
type CalendarList struct {
	Calendars []calendar.Calendar `json:"calendars"`
}

func (s *Server) registerCalendar(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_calendars",
		Description: "List the user's calendars. A calendar's id is what other calendar tools take, and read_only says it can't be changed.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.calendarListCalendars)
}

// calendarClient returns a client and a context that bounds the tool call.
func (s *Server) calendarClient(ctx context.Context) (*calendar.Client, context.Context, context.CancelFunc, error) {
	if s.options.DialCalendar == nil {
		return nil, nil, nil, errors.New("calendars are not available")
	}
	client, err := s.options.DialCalendar()
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	return client, ctx, cancel, nil
}

func (s *Server) calendarListCalendars(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, CalendarList, error) {
	client, ctx, cancel, err := s.calendarClient(ctx)
	if err != nil {
		return nil, CalendarList{}, err
	}
	defer cancel()
	calendars, err := client.Calendars(ctx)
	if err != nil {
		return nil, CalendarList{}, err
	}
	return nil, CalendarList{Calendars: calendars}, nil
}
