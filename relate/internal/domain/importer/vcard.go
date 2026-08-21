package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-vcard"
)

// ParseVCard reads a stream of one or more vCard 3.0/4.0 entries (RFC
// 6350 / the older 3.0 dialect — go-vcard's decoder handles both) and
// returns one ImportRow per card. A card with no usable name (no FN, and
// no N to derive one from) is reported as a ValidationIssue and excluded.
func ParseVCard(r io.Reader) ([]ImportRow, []ValidationIssue, error) {
	dec := vcard.NewDecoder(r)

	var rows []ImportRow
	var issues []ValidationIssue
	cardNum := 0
	for {
		card, err := dec.Decode()
		if err == io.EOF {
			break
		}
		cardNum++
		if err != nil {
			issues = append(issues, ValidationIssue{RowNumber: cardNum, Message: "failed to parse vCard entry: " + err.Error()})
			continue
		}

		row := ImportRow{RowNumber: cardNum}
		row.DisplayName = card.PreferredValue(vcard.FieldFormattedName)
		if n := card.Name(); n != nil {
			row.GivenName = n.GivenName
			row.FamilyName = n.FamilyName
		}
		if row.DisplayName == "" {
			row.DisplayName = strings.TrimSpace(row.GivenName + " " + row.FamilyName)
		}
		if row.DisplayName == "" {
			issues = append(issues, ValidationIssue{RowNumber: cardNum, Field: "FN", Message: "card has no FN or N to derive a display name from"})
			continue
		}

		if org := card.Value(vcard.FieldOrganization); org != "" {
			// ORG's first component is the organization name; later
			// components are organizational units, which the beta does
			// not model separately.
			row.OrganizationName = strings.SplitN(org, ";", 2)[0]
		}
		row.Title = card.Value(vcard.FieldTitle)
		row.Emails = card.Values(vcard.FieldEmail)
		row.Phones = card.Values(vcard.FieldTelephone)
		row.URLs = card.Values(vcard.FieldURL)

		if uid := card.Value(vcard.FieldUID); uid != "" {
			row.SourceRef = "vcard-uid:" + uid
		} else {
			row.SourceRef = vcardRowHash(row)
		}

		rows = append(rows, row)
	}

	return rows, issues, nil
}

// vcardRowHash derives a source_ref for cards without a UID, from the
// same identity fields csvRowHash uses, so an unchanged re-import of a
// UID-less card is still recognized as the same source.
func vcardRowHash(row ImportRow) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|%s", row.DisplayName, row.GivenName, row.FamilyName, strings.Join(row.Emails, ","))
	return "vcard-hash:" + hex.EncodeToString(h.Sum(nil))[:16]
}
