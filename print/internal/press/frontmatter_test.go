package press

import (
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantFM   bool
		wantBody string
	}{
		{"with frontmatter", "---\nprofile: report\n---\nhello\n", true, "hello\n"},
		{"closing dots", "---\nprofile: report\n...\nbody", true, "body"},
		{"no frontmatter", "# just markdown\n", false, "# just markdown\n"},
		{"crlf normalized", "---\r\nprofile: report\r\n---\r\nbody\r\n", true, "body\n"},
		{"unterminated is body", "---\nprofile: report\nno close", false, "---\nprofile: report\nno close"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body := Split([]byte(tt.src))
			if (len(fm) > 0) != tt.wantFM {
				t.Errorf("fm presence = %v, want %v (fm=%q)", len(fm) > 0, tt.wantFM, fm)
			}
			if string(body) != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestParseRejectsUnknownKey(t *testing.T) {
	_, err := Parse([]byte("---\nprofile: report\ntitle: X\nunknown_key: 1\n---\nbody\n"))
	if err == nil {
		t.Fatal("expected error for unknown frontmatter key")
	}
	ce, ok := err.(*ContractError)
	if !ok {
		t.Fatalf("want *ContractError, got %T", err)
	}
	if !strings.Contains(ce.Error(), "unknown_key") {
		t.Errorf("error should name the offending key: %v", ce)
	}
}

func TestParseAuthorScalarOrList(t *testing.T) {
	scalar, err := Parse([]byte("---\nauthor: Jane\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scalar.Front.Author) != 1 || scalar.Front.Author[0] != "Jane" {
		t.Errorf("scalar author = %v", scalar.Front.Author)
	}
	list, err := Parse([]byte("---\nauthor: [Jane, John]\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Front.Author) != 2 {
		t.Errorf("list author = %v", list.Front.Author)
	}
}

func TestValidate(t *testing.T) {
	behoerde, _ := Lookup("behoerde")

	good := mustParse(t, "---\nprofile: behoerde\nlang: de\ntitle: T\ndate: 30.06.2026\nrecipient:\n  name: R\n---\n")
	if issues := good.Validate(behoerde); hasErrors(issues) {
		t.Errorf("valid doc reported errors: %v", issues)
	}

	bad := mustParse(t, "---\nprofile: behoerde\ndate: 30/06/2026\n---\n")
	issues := bad.Validate(behoerde)
	if !hasErrors(issues) {
		t.Fatal("expected validation errors")
	}

	// Verify exact error messages for each required field
	expectedMsgs := map[string][]string{
		"recipient": {`profile "behoerde" requires "recipient"`},
		"title":     {`profile "behoerde" requires "title"`, `PDF/UA requires a document title in metadata`},
		"lang":      {`profile "behoerde" requires "lang"`, `PDF/UA requires a document language (e.g. lang: de)`},
		"date":      {`must be TT.MM.JJJJ or JJJJ-MM-TT with leading zeros (DIN 5008)`},
	}
	for _, issue := range issues {
		if issue.Severity != "error" {
			continue
		}
		wantMsgs, ok := expectedMsgs[issue.Field]
		if !ok {
			continue
		}
		found := false
		for _, wantMsg := range wantMsgs {
			if issue.Message == wantMsg {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("field %q: got unexpected message %q", issue.Field, issue.Message)
		}
	}
}

func TestValidateDateFormats(t *testing.T) {
	report, _ := Lookup("report")
	for _, d := range []string{"30.06.2026", "2026-06-30"} {
		doc := mustParse(t, "---\nprofile: report\ntitle: T\ndate: "+d+"\n---\n")
		if hasErrors(doc.Validate(report)) {
			t.Errorf("date %q should be valid", d)
		}
	}
	for _, tt := range []struct {
		date    string
		wantMsg string
	}{
		{"2026/06/30", `must be TT.MM.JJJJ or JJJJ-MM-TT with leading zeros (DIN 5008)`},
		{"1.1.2026", `must be TT.MM.JJJJ or JJJJ-MM-TT with leading zeros (DIN 5008)`},
		{"30-06-2026", `must be TT.MM.JJJJ or JJJJ-MM-TT with leading zeros (DIN 5008)`},
	} {
		doc := mustParse(t, "---\nprofile: report\ntitle: T\ndate: "+tt.date+"\n---\n")
		issues := doc.Validate(report)
		if !hasErrors(issues) {
			t.Errorf("date %q should be invalid", tt.date)
			continue
		}
		for _, issue := range issues {
			if issue.Field == "date" && issue.Message != tt.wantMsg {
				t.Errorf("date %q: got message %q, want %q", tt.date, issue.Message, tt.wantMsg)
			}
		}
	}
}

func TestValidateMeetingProfile(t *testing.T) {
	meeting, ok := Lookup("meeting")
	if !ok {
		t.Fatal("meeting profile not registered")
	}

	good := mustParse(t, `---
profile: meeting
title: "Project Meeting"
lang: en
date: 2026-07-16
meeting_id: "SYM-42"
participants: ["Daniel", "Antigravity"]
duration: "1h 30m"
location: "Online"
---
`)
	if issues := good.Validate(meeting); hasErrors(issues) {
		t.Errorf("valid meeting doc reported errors: %v", issues)
	}

	badMissingTitle := mustParse(t, `---
profile: meeting
lang: en
date: 2026-07-16
---
`)
	if issues := badMissingTitle.Validate(meeting); !hasErrors(issues) {
		t.Error("expected error for missing title in meeting profile")
	}

	badMissingLang := mustParse(t, `---
profile: meeting
title: "Project Meeting"
date: 2026-07-16
---
`)
	if issues := badMissingLang.Validate(meeting); !hasErrors(issues) {
		t.Error("expected error for missing lang in meeting profile")
	}

	badMissingDate := mustParse(t, `---
profile: meeting
title: "Project Meeting"
lang: en
---
`)
	if issues := badMissingDate.Validate(meeting); !hasErrors(issues) {
		t.Error("expected error for missing date in meeting profile")
	}
}

func mustParse(t *testing.T, src string) *Document {
	t.Helper()
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}
