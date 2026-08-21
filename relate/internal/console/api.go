package console

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	importerdomain "github.com/danieljustus/symaira-relate/internal/domain/importer"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// maxBodySize bounds JSON request bodies to 4 MiB, matching the MCP
// transport limit. Oversized bodies are rejected before decoding.
const maxBodySize = 4 << 20 // 4 MiB

func parseRFC3339(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

// decodeJSON reads and strictly decodes a JSON request body — unknown
// fields are rejected so a client typo fails loudly instead of silently
// being ignored, the same contract the MCP tool arguments use.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errs.Invalid("console.decodeJSON", "invalid request body: "+err.Error(), err)
	}
	return nil
}

func queryPage(r *http.Request) page.Request {
	limit, _ := intQuery(r, "limit")
	offset, _ := intQuery(r, "offset")
	return page.Request{Limit: limit, Offset: offset}
}

func intQuery(r *http.Request, name string) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, false
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// -- contacts ----------------------------------------------------------

func (s *Server) handleContactsList(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.Contacts.ListPersons(r.Context(), app.ListPersonsOptions{
		Classification: contact.Classification(r.URL.Query().Get("classification")),
		Query:          r.URL.Query().Get("q"),
		Page:           queryPage(r),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleContactGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.app.Contacts.GetPerson(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type contactCreateRequest struct {
	DisplayName string `json:"display_name"`
	GivenName   string `json:"given_name"`
	FamilyName  string `json:"family_name"`
	Notes       string `json:"notes"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
}

func (s *Server) handleContactCreate(w http.ResponseWriter, r *http.Request) {
	var req contactCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.app.CreatePersonWithContactPoints(r.Context(), contact.PersonInput{
		DisplayName: req.DisplayName, GivenName: req.GivenName, FamilyName: req.FamilyName, Notes: req.Notes,
	}, req.Email, req.Phone)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

type contactUpdateRequest struct {
	DisplayName *string `json:"display_name"`
	Notes       *string `json:"notes"`
}

func (s *Server) handleContactUpdate(w http.ResponseWriter, r *http.Request) {
	var req contactUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	p, err := s.app.Contacts.UpdatePerson(r.Context(), r.PathValue("id"), contact.PersonUpdate{
		DisplayName: req.DisplayName, Notes: req.Notes,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleContactErase uses the audited privacy-erasure workflow (see
// docs/PRIVACY.md), not a bare delete — this is the console's "erase a
// contact" acceptance criterion, and it is the same audited path the CLI's
// `contact erase` uses, not a UI-owned shortcut.
func (s *Server) handleContactErase(w http.ResponseWriter, r *http.Request) {
	summary, err := s.app.Security.EraseContact(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleContactTimeline(w http.ResponseWriter, r *http.Request) {
	tl, err := s.app.Relationships.PersonTimeline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tl)
}

func (s *Server) handleContactMemberships(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Contacts.ListMembershipsByPerson(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// -- organizations -------------------------------------------------------

func (s *Server) handleOrganizationsList(w http.ResponseWriter, r *http.Request) {
	result, err := s.app.Contacts.ListOrganizations(r.Context(), app.ListOrganizationsOptions{
		Classification: contact.Classification(r.URL.Query().Get("classification")),
		Query:          r.URL.Query().Get("q"),
		Page:           queryPage(r),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleOrganizationGet(w http.ResponseWriter, r *http.Request) {
	o, err := s.app.Contacts.GetOrganization(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

type organizationCreateRequest struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
}

func (s *Server) handleOrganizationCreate(w http.ResponseWriter, r *http.Request) {
	var req organizationCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	o, err := s.app.Contacts.CreateOrganization(r.Context(), contact.OrganizationInput{Name: req.Name, Notes: req.Notes})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

type organizationUpdateRequest struct {
	Name  *string `json:"name"`
	Notes *string `json:"notes"`
}

func (s *Server) handleOrganizationUpdate(w http.ResponseWriter, r *http.Request) {
	var req organizationUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	o, err := s.app.Contacts.UpdateOrganization(r.Context(), r.PathValue("id"), contact.OrganizationUpdate{
		Name: req.Name, Notes: req.Notes,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleOrganizationErase(w http.ResponseWriter, r *http.Request) {
	summary, err := s.app.Security.EraseOrganization(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleOrganizationTimeline(w http.ResponseWriter, r *http.Request) {
	tl, err := s.app.Relationships.OrganizationTimeline(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tl)
}

func (s *Server) handleOrganizationMemberships(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Contacts.ListMembershipsByOrganization(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// -- memberships -----------------------------------------------------------

type membershipCreateRequest struct {
	PersonID       string `json:"person_id"`
	OrganizationID string `json:"organization_id"`
	Role           string `json:"role"`
	Title          string `json:"title"`
}

func (s *Server) handleMembershipCreate(w http.ResponseWriter, r *http.Request) {
	var req membershipCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if req.PersonID == "" || req.OrganizationID == "" {
		writeErr(w, errs.Invalid("console.handleMembershipCreate", "person_id and organization_id are required", nil))
		return
	}
	m, err := s.app.Contacts.AddMembership(r.Context(), req.PersonID, req.OrganizationID, contact.MembershipInput{
		Role: req.Role, Title: req.Title,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// -- follow-ups --------------------------------------------------------------

func (s *Server) handleFollowUpsList(w http.ResponseWriter, r *http.Request) {
	personID := r.URL.Query().Get("person_id")
	orgID := r.URL.Query().Get("organization_id")
	if (personID == "") == (orgID == "") {
		writeErr(w, errs.Invalid("console.handleFollowUpsList", "exactly one of person_id or organization_id is required", nil))
		return
	}
	filter := app.FollowUpFilter(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = app.FollowUpFilterAll
	}

	var (
		list []relationship.FollowUp
		err  error
	)
	if personID != "" {
		list, err = s.app.Relationships.ListPersonFollowUps(r.Context(), personID, filter)
	} else {
		list, err = s.app.Relationships.ListOrganizationFollowUps(r.Context(), orgID, filter)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type followUpCreateRequest struct {
	PersonID       string `json:"person_id"`
	OrganizationID string `json:"organization_id"`
	DueAt          string `json:"due_at"`
	Notes          string `json:"notes"`
}

func (s *Server) handleFollowUpCreate(w http.ResponseWriter, r *http.Request) {
	var req followUpCreateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if (req.PersonID == "") == (req.OrganizationID == "") {
		writeErr(w, errs.Invalid("console.handleFollowUpCreate", "exactly one of person_id or organization_id is required", nil))
		return
	}
	dueAt, err := parseRFC3339(req.DueAt)
	if err != nil {
		writeErr(w, errs.Invalid("console.handleFollowUpCreate", "due_at: "+err.Error(), err))
		return
	}

	in := relationship.FollowUpInput{DueAt: dueAt, Notes: req.Notes}
	var out *relationship.FollowUp
	if req.PersonID != "" {
		out, err = s.app.Relationships.AddPersonFollowUp(r.Context(), req.PersonID, in)
	} else {
		out, err = s.app.Relationships.AddOrganizationFollowUp(r.Context(), req.OrganizationID, in)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleFollowUpComplete(w http.ResponseWriter, r *http.Request) {
	fu, err := s.app.Relationships.CompleteFollowUp(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fu)
}

func (s *Server) handleFollowUpCancel(w http.ResponseWriter, r *http.Request) {
	fu, err := s.app.Relationships.CancelFollowUp(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fu)
}

// -- import (review + reviewed dedup apply) -----------------------------

// importRequest is shared by /import/plan and /import/apply: Path is a
// file on the same machine the console runs on (this is a single-user,
// localhost-only tool — there is no upload step, exactly like the CLI
// reading a local file path). See docs/CONSOLE.md.
type importRequest struct {
	Path        string                         `json:"path"`
	Kind        string                         `json:"kind"` // "vcard" or "csv"
	Map         map[string]string              `json:"map,omitempty"`
	Resolutions []importerdomain.RowResolution `json:"resolutions,omitempty"`
}

func parseImportSource(req importRequest) (importerdomain.SourceKind, []importerdomain.ImportRow, []importerdomain.ValidationIssue, error) {
	const op = "console.parseImportSource"
	f, err := os.Open(req.Path)
	if err != nil {
		return "", nil, nil, errs.Invalid(op, "failed to open import file: "+err.Error(), err)
	}
	defer f.Close()

	switch req.Kind {
	case string(importerdomain.SourceVCard):
		rows, issues, err := importerdomain.ParseVCard(f)
		return importerdomain.SourceVCard, rows, issues, err
	case string(importerdomain.SourceCSV):
		mapping := importerdomain.ColumnMapping{}
		if len(req.Map) == 0 {
			detected, err := detectMappingFromPath(req.Path)
			if err != nil {
				return "", nil, nil, err
			}
			mapping = detected
		} else {
			mapping = columnMappingFromRequest(req.Map)
		}
		rows, issues, err := importerdomain.ParseCSV(f, mapping)
		return importerdomain.SourceCSV, rows, issues, err
	default:
		return "", nil, nil, errs.Invalid(op, "kind must be \"vcard\" or \"csv\"", nil)
	}
}

// detectMappingFromPath re-opens path to peek its header row — parseImportSource's
// caller already holds f positioned at the start for ParseCSV's own read, so
// detection uses its own handle rather than consuming f's.
func detectMappingFromPath(path string) (importerdomain.ColumnMapping, error) {
	const op = "console.detectMappingFromPath"
	hf, err := os.Open(path)
	if err != nil {
		return importerdomain.ColumnMapping{}, errs.Invalid(op, "failed to open import file: "+err.Error(), err)
	}
	defer hf.Close()

	header, err := csv.NewReader(hf).Read()
	if err == io.EOF {
		return importerdomain.ColumnMapping{}, errs.Invalid(op, "csv file is empty", nil)
	}
	if err != nil {
		return importerdomain.ColumnMapping{}, errs.Invalid(op, "failed to read CSV header: "+err.Error(), err)
	}
	return importerdomain.DetectColumnMapping(header), nil
}

func columnMappingFromRequest(m map[string]string) importerdomain.ColumnMapping {
	return importerdomain.ColumnMapping{
		DisplayName:  m["name"],
		GivenName:    m["given"],
		FamilyName:   m["family"],
		Organization: m["org"],
		Title:        m["title"],
		Email:        m["email"],
		Phone:        m["phone"],
		URL:          m["url"],
	}
}

func (s *Server) handleImportPlan(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	source, rows, issues, err := parseImportSource(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	plan, err := s.app.Import.Plan(r.Context(), source, rows, issues)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) handleImportApply(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, err)
		return
	}
	source, rows, issues, err := parseImportSource(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	plan, err := s.app.Import.Plan(r.Context(), source, rows, issues)
	if err != nil {
		writeErr(w, err)
		return
	}
	result, err := s.app.Import.Apply(r.Context(), plan, req.Resolutions)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Plan   *importerdomain.ImportPlan `json:"plan"`
		Result any                        `json:"result"`
	}{Plan: plan, Result: result})
}

func (s *Server) handleImportRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.app.Import.ListRuns(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}
