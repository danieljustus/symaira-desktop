package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/domain/security"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// tool is one MCP tool: its advertised schema plus the handler dispatched
// by tools/call. handler receives raw JSON arguments so each handler
// controls its own strict decoding and validation — an oversized or
// malformed argument object never reaches the service layer.
type tool struct {
	name        string
	description string
	inputSchema map[string]any
	handler     func(ctx context.Context, a *app.App, args json.RawMessage) (any, error)
}

// registerTools registers the reviewed 8-tool catalog on the corekit MCP
// server. Registration order is the tools/list order (alphabetical).
// Handler errors are mapped through redactErr before they can reach a
// client — mcpserver reports them in-band (isError: true), so the PII
// boundary lives here, exactly where the old errorToResponse path kept it.
func (s *Server) registerTools() {
	for _, t := range []tool{
		contactSearchTool(),
		contactGetTool(),
		contactCreateTool(),
		contactUpdateTool(),
		organizationSearchTool(),
		organizationGetTool(),
		followUpListTool(),
		timelineGetTool(),
	} {
		schema, err := json.Marshal(t.inputSchema)
		if err != nil {
			panic("mcp: marshal input schema for " + t.name + ": " + err.Error())
		}
		s.srv.RegisterTool(&mcpserver.Tool{
			Name:        t.name,
			Description: t.description,
			InputSchema: schema,
			Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
				result, err := t.handler(ctx, s.app, input)
				if err != nil {
					return nil, redactErr(err)
				}
				return result, nil
			},
		})
	}
}

// redactErr maps a handler error to its client-visible form. Every error
// message that can reach an MCP client passes through security.Redact
// (see docs/PRIVACY.md), so a contact-point value that reached an error
// path — a duplicate email in a conflict error, for example — can never
// leak through the transport.
func redactErr(err error) error {
	return errors.New(security.Redact(err.Error()))
}

// -- contact_search ------------------------------------------------------

func contactSearchTool() tool {
	return tool{
		name: "contact_search",
		description: "Search and list people by name substring and/or classification, paginated. Read-only. " +
			"Never use this to bulk-ingest contacts into another system — results are for the calling agent's " +
			"immediate task only.",
		inputSchema: objectSchema(map[string]any{
			"query":          stringProp("Case-insensitive substring matched against display/given/family name"),
			"classification": stringProp("Filter: personal, business, customer, lead, or partner"),
			"limit":          intProp("Max results (server-bounded; see docs/CLI_CONTRACT.md)"),
			"offset":         intProp("Result offset for pagination"),
		}, nil),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				Query          string `json:"query"`
				Classification string `json:"classification"`
				Limit          int    `json:"limit"`
				Offset         int    `json:"offset"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			return a.Contacts.ListPersons(ctx, app.ListPersonsOptions{
				Classification: contact.Classification(args.Classification),
				Query:          args.Query,
				Page:           page.Request{Limit: args.Limit, Offset: args.Offset},
			})
		},
	}
}

// -- contact_get -----------------------------------------------------------

func contactGetTool() tool {
	return tool{
		name:        "contact_get",
		description: "Load one person by id, including contact points, aliases, tags and classifications. Read-only. Never expose sensitive contact-point values beyond what this call explicitly returns.",
		inputSchema: objectSchema(map[string]any{
			"id": stringProp("Person id"),
		}, []string{"id"}),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				ID string `json:"id"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.ID == "" {
				return nil, errs.Invalid("mcp.contact_get", "id is required", nil)
			}
			return a.Contacts.GetPerson(ctx, args.ID)
		},
	}
}

// -- contact_create --------------------------------------------------------

func contactCreateTool() tool {
	return tool{
		name: "contact_create",
		description: "Create a new person. This is an explicit, deliberate mutation — never call this to auto-ingest " +
			"contacts discovered incidentally (a meeting attendee, an email sender); only call it when the user has " +
			"asked to add this specific person.",
		inputSchema: objectSchema(map[string]any{
			"display_name": stringProp("Display name (required)"),
			"given_name":   stringProp("Given name"),
			"family_name":  stringProp("Family name"),
			"notes":        stringProp("Free-text notes"),
			"email":        stringProp("Email contact point"),
			"phone":        stringProp("Phone contact point"),
		}, []string{"display_name"}),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				DisplayName string `json:"display_name"`
				GivenName   string `json:"given_name"`
				FamilyName  string `json:"family_name"`
				Notes       string `json:"notes"`
				Email       string `json:"email"`
				Phone       string `json:"phone"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			return a.CreatePersonWithContactPoints(ctx, contact.PersonInput{
				DisplayName: args.DisplayName, GivenName: args.GivenName, FamilyName: args.FamilyName, Notes: args.Notes,
			}, args.Email, args.Phone)
		},
	}
}

// -- contact_update --------------------------------------------------------

func contactUpdateTool() tool {
	return tool{
		name:        "contact_update",
		description: "Patch an existing person's display name and/or notes. Only fields provided are changed. Explicit, deliberate mutation only.",
		inputSchema: objectSchema(map[string]any{
			"id":           stringProp("Person id (required)"),
			"display_name": stringProp("New display name"),
			"notes":        stringProp("New notes"),
		}, []string{"id"}),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				ID          string  `json:"id"`
				DisplayName *string `json:"display_name"`
				Notes       *string `json:"notes"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.ID == "" {
				return nil, errs.Invalid("mcp.contact_update", "id is required", nil)
			}
			return a.Contacts.UpdatePerson(ctx, args.ID, contact.PersonUpdate{
				DisplayName: args.DisplayName, Notes: args.Notes,
			})
		},
	}
}

// -- organization_search / organization_get --------------------------------

func organizationSearchTool() tool {
	return tool{
		name:        "organization_search",
		description: "Search and list organizations by name substring and/or classification, paginated. Read-only.",
		inputSchema: objectSchema(map[string]any{
			"query":          stringProp("Case-insensitive substring matched against the organization name"),
			"classification": stringProp("Filter: personal, business, customer, lead, or partner"),
			"limit":          intProp("Max results (server-bounded)"),
			"offset":         intProp("Result offset for pagination"),
		}, nil),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				Query          string `json:"query"`
				Classification string `json:"classification"`
				Limit          int    `json:"limit"`
				Offset         int    `json:"offset"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			return a.Contacts.ListOrganizations(ctx, app.ListOrganizationsOptions{
				Classification: contact.Classification(args.Classification),
				Query:          args.Query,
				Page:           page.Request{Limit: args.Limit, Offset: args.Offset},
			})
		},
	}
}

func organizationGetTool() tool {
	return tool{
		name:        "organization_get",
		description: "Load one organization by id, including contact points, aliases, tags and classifications. Read-only.",
		inputSchema: objectSchema(map[string]any{
			"id": stringProp("Organization id"),
		}, []string{"id"}),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				ID string `json:"id"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			if args.ID == "" {
				return nil, errs.Invalid("mcp.organization_get", "id is required", nil)
			}
			return a.Contacts.GetOrganization(ctx, args.ID)
		},
	}
}

// -- follow_up_list ----------------------------------------------------------

func followUpListTool() tool {
	return tool{
		name: "followup_list",
		description: "List follow-up reminders for a person or organization, optionally filtered to open/overdue/" +
			"upcoming. Read-only.",
		inputSchema: objectSchema(map[string]any{
			"person_id":       stringProp("Person id — exactly one of person_id/organization_id is required"),
			"organization_id": stringProp("Organization id — exactly one of person_id/organization_id is required"),
			"filter":          stringProp("all (default), open, overdue, or upcoming"),
		}, nil),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				PersonID       string `json:"person_id"`
				OrganizationID string `json:"organization_id"`
				Filter         string `json:"filter"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			if (args.PersonID == "") == (args.OrganizationID == "") {
				return nil, errs.Invalid("mcp.followup_list", "exactly one of person_id or organization_id is required", nil)
			}
			filter := app.FollowUpFilter(args.Filter)
			if filter == "" {
				filter = app.FollowUpFilterAll
			}
			if args.PersonID != "" {
				return a.Relationships.ListPersonFollowUps(ctx, args.PersonID, filter)
			}
			return a.Relationships.ListOrganizationFollowUps(ctx, args.OrganizationID, filter)
		},
	}
}

// -- timeline_get ------------------------------------------------------------

func timelineGetTool() tool {
	return tool{
		name:        "timeline_get",
		description: "Get a person's or organization's combined interaction and follow-up timeline, most recent first. Read-only.",
		inputSchema: objectSchema(map[string]any{
			"person_id":       stringProp("Person id — exactly one of person_id/organization_id is required"),
			"organization_id": stringProp("Organization id — exactly one of person_id/organization_id is required"),
		}, nil),
		handler: func(ctx context.Context, a *app.App, raw json.RawMessage) (any, error) {
			var args struct {
				PersonID       string `json:"person_id"`
				OrganizationID string `json:"organization_id"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return nil, err
			}
			if (args.PersonID == "") == (args.OrganizationID == "") {
				return nil, errs.Invalid("mcp.timeline_get", "exactly one of person_id or organization_id is required", nil)
			}
			if args.PersonID != "" {
				return a.Relationships.PersonTimeline(ctx, args.PersonID)
			}
			return a.Relationships.OrganizationTimeline(ctx, args.OrganizationID)
		},
	}
}

// -- shared helpers ----------------------------------------------------------

func decodeArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errs.Invalid("mcp.decodeArgs", "invalid arguments: "+err.Error(), err)
	}
	return nil
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}
