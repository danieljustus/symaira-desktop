package press

import "testing"

func TestBuiltinProfilesPresent(t *testing.T) {
	for _, name := range []string{"brief", "behoerde", "report", "rechnung"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("missing built-in profile %q", name)
		}
		if p.Template == "" || p.Engine == "" {
			t.Errorf("profile %q missing template/engine", name)
		}
	}
}

func TestBehoerdeCapabilities(t *testing.T) {
	p, _ := Lookup("behoerde")
	c := p.Capability()
	if !c.PDFA || !c.PDFUA || !c.DINWindow {
		t.Errorf("behoerde should guarantee PDF/A + PDF/UA + DIN window, got %+v", c)
	}
	if !p.RequiresAccessibility() {
		t.Error("behoerde should require accessibility")
	}
}

func TestReportIsNotAccessibilityGated(t *testing.T) {
	p, _ := Lookup("report")
	if p.RequiresAccessibility() {
		t.Error("report should not be accessibility-gated by default")
	}
	if p.Capability().PDFA {
		t.Error("report should not claim PDF/A by default")
	}
}

func TestAllSorted(t *testing.T) {
	all := All()
	if len(all) < 4 {
		t.Fatalf("expected >=4 profiles, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Name > all[i].Name {
			t.Errorf("All() not sorted: %q before %q", all[i-1].Name, all[i].Name)
		}
	}
}
