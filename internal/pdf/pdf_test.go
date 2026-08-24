package pdf

import (
	"context"
	"testing"
)

func TestRenderPassesOptions(t *testing.T) {
	prevRender := RenderFunc
	t.Cleanup(func() { RenderFunc = prevRender })

	var gotSource []byte
	var gotOut string
	var gotOpts Options
	RenderFunc = func(_ context.Context, src []byte, out string, opts Options) (*Result, error) {
		gotSource = src
		gotOut = out
		gotOpts = opts
		return &Result{
			OutputPath:    out,
			Profile:       opts.Profile,
			EngineVersion: "1.0.0",
		}, nil
	}

	res, err := Render([]byte("# Heading"), "/tmp/out.pdf", "report", "/path/to/source")
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if string(gotSource) != "# Heading" {
		t.Errorf("got source %q, want %q", gotSource, "# Heading")
	}
	if gotOut != "/tmp/out.pdf" {
		t.Errorf("got out %q, want %q", gotOut, "/tmp/out.pdf")
	}
	if gotOpts.Profile != "report" {
		t.Errorf("got profile %q, want %q", gotOpts.Profile, "report")
	}
	if gotOpts.SourceDir != "/path/to/source" {
		t.Errorf("got SourceDir %q, want %q", gotOpts.SourceDir, "/path/to/source")
	}
	if res.OutputPath != "/tmp/out.pdf" {
		t.Errorf("got res.OutputPath %q, want %q", res.OutputPath, "/tmp/out.pdf")
	}
}

func TestEngineAvailable(t *testing.T) {
	prev := EngineAvailableFunc
	t.Cleanup(func() { EngineAvailableFunc = prev })

	EngineAvailableFunc = func(context.Context) (bool, string) {
		return true, "0.15.0"
	}
	ok, ver := EngineAvailable()
	if !ok || ver != "0.15.0" {
		t.Errorf("got ok=%v, ver=%q, want true, 0.15.0", ok, ver)
	}
}

func TestProfiles(t *testing.T) {
	profs := Profiles()
	if len(profs) == 0 {
		t.Fatal("expected at least one profile")
	}
	found := false
	for _, p := range profs {
		if p.Name == "report" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected report profile in Profiles()")
	}
}
