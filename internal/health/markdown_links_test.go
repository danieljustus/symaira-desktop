package health

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type portMarkdownTarget struct {
	Destination string `json:"destination"`
	Target      string `json:"target"`
	Checked     bool   `json:"checked"`
	Exists      bool   `json:"exists"`
}
type portMarkdownCase struct {
	Name    string               `json:"name"`
	Body    string               `json:"body"`
	Files   []string             `json:"files"`
	Targets []portMarkdownTarget `json:"targets"`
}

func runMarkdownHealthCases(t *testing.T) []portMarkdownCase {
	t.Helper()
	files := []string{"assets/photo.png", "docs/manual.pdf", "assets/a(b).png", "assets/a b.png", "assets/literal#name.png", "assets/überblick_日本.png", "assets/a&b.png", "folder/note.md", ".hidden.png", ".trash/gone.pdf", "node_modules/ignored.pdf"}
	local := func(destination, target string, exists bool) portMarkdownTarget {
		return portMarkdownTarget{Destination: destination, Target: target, Checked: true, Exists: exists}
	}
	remote := func(destination string) portMarkdownTarget { return portMarkdownTarget{Destination: destination} }
	cases := []portMarkdownCase{
		{Name: "link-and-image", Body: `[file](docs/manual.pdf) ![image](assets/photo.png)`, Targets: []portMarkdownTarget{local("docs/manual.pdf", "docs/manual.pdf", true), local("assets/photo.png", "assets/photo.png", true)}},
		{Name: "missing-attachments", Body: `[file](docs/absent.pdf) ![image](assets/absent.png)`, Targets: []portMarkdownTarget{local("docs/absent.pdf", "docs/absent.pdf", false), local("assets/absent.png", "assets/absent.png", false)}},
		{Name: "titles", Body: `[file](docs/manual.pdf "Quarterly report") ![image](assets/photo.png 'Photo')`, Targets: []portMarkdownTarget{local("docs/manual.pdf", "docs/manual.pdf", true), local("assets/photo.png", "assets/photo.png", true)}},
		{Name: "escaped-parentheses", Body: `![image](assets/a\(b\).png)`, Targets: []portMarkdownTarget{local("assets/a(b).png", "assets/a(b).png", true)}},
		{Name: "nested-parentheses", Body: `![image](assets/a(b).png)`, Targets: []portMarkdownTarget{local("assets/a(b).png", "assets/a(b).png", true)}},
		{Name: "angle-space", Body: `![image](<assets/a b.png>)`, Targets: []portMarkdownTarget{local("assets/a b.png", "assets/a b.png", true)}},
		{Name: "encoded-space-and-fragment", Body: `![image](assets/a%20b.png#caption)`, Targets: []portMarkdownTarget{local("assets/a%20b.png#caption", "assets/a b.png", true)}},
		{Name: "encoded-literal-fragment", Body: `![image](assets/literal%23name.png#caption)`, Targets: []portMarkdownTarget{local("assets/literal%23name.png#caption", "assets/literal#name.png", true)}},
		{Name: "unicode-case", Body: `![image](assets/ÜBERBLICK_日本.PNG)`, Targets: []portMarkdownTarget{local("assets/ÜBERBLICK_日本.PNG", "assets/ÜBERBLICK_日本.PNG", true)}},
		{Name: "entity", Body: `![image](assets/a&amp;b.png)`, Targets: []portMarkdownTarget{local("assets/a&b.png", "assets/a&b.png", true)}},
		{Name: "reference", Body: "[file][report]\n![picture][photo]\n\n[report]: docs/manual.pdf \"Report\"\n[photo]: assets/photo.png", Targets: []portMarkdownTarget{local("docs/manual.pdf", "docs/manual.pdf", true), local("assets/photo.png", "assets/photo.png", true)}},
		{Name: "shortcut-reference", Body: "[report]\n\n[report]: docs/manual.pdf", Targets: []portMarkdownTarget{local("docs/manual.pdf", "docs/manual.pdf", true)}},
		{Name: "remote-schemes", Body: `[web](https://example.test/missing.pdf) ![cdn](//cdn.example.test/image.png) [mail](mailto:a@example.test) [data](data:image/png;base64,abc) [ftp](FTP://example.test/file.pdf)`, Targets: []portMarkdownTarget{remote("https://example.test/missing.pdf"), remote("//cdn.example.test/image.png"), remote("mailto:a@example.test"), remote("data:image/png;base64,abc"), remote("FTP://example.test/file.pdf")}},
		{Name: "same-document", Body: `[heading](#Heading) [query](?view=1)`, Targets: []portMarkdownTarget{remote("#Heading"), remote("?view=1")}},
		{Name: "query", Body: `[file](docs/manual.pdf?download=1#page=2)`, Targets: []portMarkdownTarget{local("docs/manual.pdf?download=1#page=2", "docs/manual.pdf", true)}},
		{Name: "basename", Body: `![image](PHOTO.PNG)`, Targets: []portMarkdownTarget{local("PHOTO.PNG", "PHOTO.PNG", true)}},
		{Name: "wrong-folder", Body: `![image](wrong/photo.png)`, Targets: []portMarkdownTarget{local("wrong/photo.png", "wrong/photo.png", false)}},
		{Name: "relative-clean", Body: `![image](./assets/../assets/photo.png)`, Targets: []portMarkdownTarget{local("./assets/../assets/photo.png", "./assets/../assets/photo.png", true)}},
		{Name: "traversal", Body: `[file](../docs/manual.pdf) [encoded](%2e%2e/docs/manual.pdf) [absolute](/docs/manual.pdf) [backslash](..%5cmanual.pdf)`, Targets: []portMarkdownTarget{local("../docs/manual.pdf", "../docs/manual.pdf", false), local("%2e%2e/docs/manual.pdf", "../docs/manual.pdf", false), local("/docs/manual.pdf", "/docs/manual.pdf", false), local("..%5cmanual.pdf", "..\\manual.pdf", false)}},
		{Name: "ignored-files", Body: `![hidden](.hidden.png) [trash](.trash/gone.pdf) [dependency](node_modules/ignored.pdf)`, Targets: []portMarkdownTarget{local(".hidden.png", ".hidden.png", false), local(".trash/gone.pdf", ".trash/gone.pdf", false), local("node_modules/ignored.pdf", "node_modules/ignored.pdf", false)}},
		{Name: "code-and-escaped-syntax", Body: "`[inline](absent.pdf)`\n\n```md\n![fenced](absent.png)\n```\n\n    [indented](absent.pdf)\n\n\\[escaped](absent.pdf)\n", Targets: []portMarkdownTarget{}},
		{Name: "raw-html", Body: `<a href="absent.pdf">file</a><img src="absent.png">`, Targets: []portMarkdownTarget{}},
		{Name: "unresolved-reference", Body: `[file][undefined]`, Targets: []portMarkdownTarget{}},
		{Name: "document", Body: `[note](folder/note.md#Heading)`, Targets: []portMarkdownTarget{local("folder/note.md#Heading", "folder/note.md", true)}},
		{Name: "malformed-percent", Body: `[malformed](assets/%zz.png) [invalid](assets/%FF.png)`, Targets: []portMarkdownTarget{local("assets/%zz.png", "assets/%zz.png", false), local("assets/%FF.png", "assets/%FF.png", false)}},
		{Name: "autolinks", Body: `<https://example.test/absent.pdf> <a@example.test>`, Targets: []portMarkdownTarget{remote("https://example.test/absent.pdf"), remote("mailto:a@example.test")}},
		{Name: "tilde-fence-and-html-block", Body: "~~~md\n[code](absent.pdf)\n~~~\n\n<!--\n![hidden](absent.png)\n-->\n", Targets: []portMarkdownTarget{}},

		{Name: "legacy-wiki", Body: `![[assets/photo.png]] [[missing.md]]`, Targets: []portMarkdownTarget{}},
	}
	for i := range cases {
		item := &cases[i]
		item.Files = append([]string(nil), files...)
		root := t.TempDir()
		for _, file := range item.Files {
			absolute := filepath.Join(root, filepath.FromSlash(file))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
				t.Fatal(err)
			}
			content := "fixture attachment"
			if filepath.Ext(file) == ".md" {
				content = "---\ntitle: Existing\n---\n"
			}
			if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		inventory := make(map[string][]string)
		if err := vault.WalkAll(root, func(absolute string, _ fs.DirEntry) error {
			relative, err := filepath.Rel(root, absolute)
			if err == nil {
				addMarkdownFile(inventory, filepath.ToSlash(relative))
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		actual := []portMarkdownTarget{}
		for _, destination := range extractMarkdownLinks(item.Body) {
			target, checked := markdownLinkTarget(destination)
			actual = append(actual, portMarkdownTarget{Destination: destination, Target: target, Checked: checked, Exists: checked && markdownLinkExists(root, target, inventory)})
		}
		if !reflect.DeepEqual(actual, item.Targets) {
			t.Fatalf("%s targets: got %+v, want %+v", item.Name, actual, item.Targets)
		}
		note := filepath.Join(root, "health.md")
		if err := os.WriteFile(note, []byte("---\ntitle: Health\n---\n"+item.Body), 0o600); err != nil {
			t.Fatal(err)
		}
		report, err := Scan(root, nil, 90)
		if err != nil {
			t.Fatal(err)
		}
		missing := []string{}
		for _, target := range actual {
			if target.Checked && !target.Exists {
				missing = append(missing, fmt.Sprintf("Markdown target %q does not resolve to a vault file", target.Target))
			}
		}
		got := []string{}
		for _, finding := range report.Findings {
			if finding.Category == "broken_markdown_link" {
				got = append(got, finding.Message)
			}
		}
		if !reflect.DeepEqual(got, missing) {
			t.Fatalf("%s scan: got %v, want %v", item.Name, got, missing)
		}
	}
	return cases
}

func TestMarkdownAttachmentHealth(t *testing.T) { runMarkdownHealthCases(t) }

func TestMarkdownAttachmentSymlinkConfinement(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.pdf")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.pdf")); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	if markdownLinkExists(root, "escape.pdf", map[string][]string{"escape.pdf": {"escape.pdf"}}) {
		t.Fatal("external attachment symlink must not resolve")
	}
}
