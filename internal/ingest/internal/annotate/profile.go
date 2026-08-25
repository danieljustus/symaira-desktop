package annotate

import "fmt"

// builtinProfiles is the registry of built-in extraction profiles.
var builtinProfiles = map[string]Profile{
	"generic":   &genericProfile{},
	"invoice":   &invoiceProfile{},
	"contract":  &contractProfile{},
	"jobcenter": &jobcenterProfile{},
}

// GetProfile returns the named built-in profile, or an error if unknown.
func GetProfile(name string) (Profile, error) {
	p, ok := builtinProfiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown extraction profile %q", name)
	}
	return p, nil
}

// ProfileNames returns the list of available built-in profile names.
func ProfileNames() []string {
	names := make([]string, 0, len(builtinProfiles))
	for k := range builtinProfiles {
		names = append(names, k)
	}
	return names
}

// genericProfile extracts common fields: dates, amounts, emails, URLs, IBANs.
type genericProfile struct{}

func (p *genericProfile) Name() string { return "generic" }
func (p *genericProfile) Fields() []Field {
	return []Field{
		{Name: "date", Type: "date", Description: "Date"},
		{Name: "amount", Type: "amount", Description: "Monetary amount"},
		{Name: "email", Type: "email", Description: "Email address"},
		{Name: "url", Type: "url", Description: "URL"},
		{Name: "iban", Type: "iban", Description: "IBAN"},
		{Name: "phone", Type: "text", Description: "Phone number"},
	}
}

// invoiceProfile extends generic with invoice-specific fields.
type invoiceProfile struct{}

func (p *invoiceProfile) Name() string { return "invoice" }
func (p *invoiceProfile) Fields() []Field {
	return []Field{
		{Name: "date", Type: "date", Description: "Date"},
		{Name: "amount", Type: "amount", Description: "Monetary amount"},
		{Name: "invoice_number", Type: "id", Description: "Invoice number"},
		{Name: "total", Type: "amount", Description: "Total amount"},
		{Name: "due_date", Type: "deadline", Description: "Payment due date"},
		{Name: "email", Type: "email", Description: "Email address"},
		{Name: "iban", Type: "iban", Description: "IBAN"},
		{Name: "organization", Type: "party", Description: "Organization name"},
	}
}

// contractProfile extends generic with contract-specific fields.
type contractProfile struct{}

func (p *contractProfile) Name() string { return "contract" }
func (p *contractProfile) Fields() []Field {
	return []Field{
		{Name: "date", Type: "date", Description: "Date"},
		{Name: "deadline", Type: "deadline", Description: "Deadline"},
		{Name: "party", Type: "party", Description: "Contracting party"},
		{Name: "email", Type: "email", Description: "Email address"},
		{Name: "amount", Type: "amount", Description: "Contract value"},
		{Name: "organization", Type: "party", Description: "Organization"},
	}
}

// jobcenterProfile extends generic with job-center-specific fields.
type jobcenterProfile struct{}

func (p *jobcenterProfile) Name() string { return "jobcenter" }
func (p *jobcenterProfile) Fields() []Field {
	return []Field{
		{Name: "date", Type: "date", Description: "Date"},
		{Name: "job_id", Type: "id", Description: "Job/case ID"},
		{Name: "appointment_date", Type: "date", Description: "Appointment date"},
		{Name: "deadline", Type: "deadline", Description: "Deadline"},
		{Name: "email", Type: "email", Description: "Email address"},
		{Name: "organization", Type: "party", Description: "Organization"},
	}
}
