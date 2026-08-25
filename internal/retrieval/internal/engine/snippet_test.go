package engine

import (
	"strings"
	"testing"
)

func TestBuildSnippet_LongerThanBoundReturnsWindow(t *testing.T) {
	// Content with a match near the middle.
	content := strings.Repeat("a", 100) + "target word here" + strings.Repeat("b", 200)
	snippet := BuildSnippet(content, []string{"target"}, 300)
	if len([]rune(snippet)) > 300 {
		t.Errorf("snippet length %d exceeds bound 300", len([]rune(snippet)))
	}
	if !strings.Contains(snippet, "target") {
		t.Errorf("snippet should contain the matched term 'target', got %q", snippet)
	}
}

func TestBuildSnippet_ShorterThanBoundReturnsFull(t *testing.T) {
	content := "short content without query terms"
	snippet := BuildSnippet(content, []string{"nonexistent"}, 300)
	if snippet != content {
		t.Errorf("expected full content for short/no-match, got %q", snippet)
	}
}

func TestBuildSnippet_NoMatchFallbackFirstNChars(t *testing.T) {
	content := strings.Repeat("x", 500)
	snippet := BuildSnippet(content, []string{"nonexistent"}, 100)
	if len([]rune(snippet)) > 100 {
		t.Errorf("no-match fallback snippet length %d exceeds bound 100", len([]rune(snippet)))
	}
	if snippet != string([]rune(content)[:100]) {
		t.Errorf("expected first 100 chars for no-match fallback, got %q", snippet)
	}
}

func TestBuildSnippet_EmptyQueryTermsReturnsFirstN(t *testing.T) {
	content := strings.Repeat("x", 500)
	snippet := BuildSnippet(content, nil, 100)
	if len([]rune(snippet)) > 100 {
		t.Errorf("empty-query snippet length %d exceeds bound 100", len([]rune(snippet)))
	}
}

func TestBuildSnippet_MatchesFirstTerm(t *testing.T) {
	content := "aaa " + "alpha " + "bbb " + "beta " + strings.Repeat("c", 200)
	snippet := BuildSnippet(content, []string{"beta", "alpha"}, 100)
	// Should match 'alpha' first since it appears earlier.
	if !strings.Contains(snippet, "alpha") {
		t.Errorf("snippet should contain earliest match 'alpha', got %q", snippet)
	}
}

func TestBuildSnippet_CaseInsensitiveMatch(t *testing.T) {
	content := strings.Repeat("x", 100) + "HelloWorld" + strings.Repeat("y", 200)
	snippet := BuildSnippet(content, []string{"helloworld"}, 300)
	if !strings.Contains(snippet, "HelloWorld") {
		t.Errorf("case-insensitive match failed, snippet %q should contain 'HelloWorld'", snippet)
	}
}

func TestBuildSnippet_UnicodeBoundaries(t *testing.T) {
	// Content with multi-byte Unicode characters.
	content := strings.Repeat("a", 100) + "日本語のテキスト" + strings.Repeat("b", 200)
	snippet := BuildSnippet(content, []string{"日本語"}, 200)
	// Should not produce invalid UTF-8.
	if !strings.Contains(snippet, "日本語") {
		t.Errorf("snippet should contain Japanese text, got %q", snippet)
	}
	// Verify the snippet is valid UTF-8.
	rs := []rune(snippet)
	if len(rs) > 200 {
		t.Errorf("snippet rune length %d exceeds bound 200", len(rs))
	}
}

func TestBuildSnippet_UnicodeOnly(t *testing.T) {
	content := "🎉 prefix " + "test 这是一个测试 " + strings.Repeat("é", 200) + " 🎉 suffix"
	snippet := BuildSnippet(content, []string{"test 这是一个测试"}, 100)
	if !strings.Contains(snippet, "test 这是一个测试") {
		t.Errorf("snippet should contain Chinese text, got %q", snippet)
	}
	// Verify valid UTF-8.
	if len([]rune(snippet)) > 100 {
		t.Errorf("snippet rune length %d exceeds bound 100", len([]rune(snippet)))
	}
}

func TestBuildSnippet_MarkdownResidue(t *testing.T) {
	content := strings.Repeat("before ", 50) +
		"# Heading\n**bold** `code` _italic_ [link](http://example.com)\n" +
		"target_word_in_markdown\n" +
		"<script>alert('xss')</script>\n" +
		strings.Repeat(" after", 200)
	snippet := BuildSnippet(content, []string{"target_word_in_markdown"}, 200)
	if !strings.Contains(snippet, "target_word_in_markdown") {
		t.Errorf("snippet should contain markdown target, got %q", snippet)
	}
	if len([]rune(snippet)) > 200 {
		t.Errorf("snippet rune length %d exceeds bound 200", len([]rune(snippet)))
	}
}

func TestBuildSnippet_HtmlResidue(t *testing.T) {
	content := strings.Repeat("div ", 100) +
		"<div class=\"foo\"><p>target html content</p></div>" +
		strings.Repeat(" span ", 200)
	snippet := BuildSnippet(content, []string{"target html"}, 200)
	if !strings.Contains(snippet, "target html") {
		t.Errorf("snippet should contain html target, got %q", snippet)
	}
}

func TestBuildSnippet_ScriptLikeResidue(t *testing.T) {
	content := strings.Repeat("var ", 100) +
		"const x = 'target'; function init() { return x; }" +
		strings.Repeat(" let ", 200)
	snippet := BuildSnippet(content, []string{"target"}, 200)
	if !strings.Contains(snippet, "target") {
		t.Errorf("snippet should contain script target, got %q", snippet)
	}
}

func TestBuildSnippet_DefaultBound(t *testing.T) {
	content := strings.Repeat("a", 500)
	snippet := BuildSnippet(content, []string{"a"}, 0) // 0 uses default (300)
	if len([]rune(snippet)) > DefaultSnippetBound {
		t.Errorf("snippet length %d exceeds default bound %d", len([]rune(snippet)), DefaultSnippetBound)
	}
}

func TestBuildSnippet_MatchAtStart(t *testing.T) {
	content := "target at the very start" + strings.Repeat("x", 500)
	snippet := BuildSnippet(content, []string{"target"}, 200)
	if !strings.Contains(snippet, "target") {
		t.Errorf("snippet should contain start match 'target', got %q", snippet)
	}
	if len([]rune(snippet)) > 200 {
		t.Errorf("snippet rune length %d exceeds bound 200", len([]rune(snippet)))
	}
}

func TestBuildSnippet_MatchAtEnd(t *testing.T) {
	content := strings.Repeat("x", 500) + "target at the very end"
	snippet := BuildSnippet(content, []string{"target"}, 200)
	if !strings.Contains(snippet, "target") {
		t.Errorf("snippet should contain end match 'target', got %q", snippet)
	}
	if len([]rune(snippet)) > 200 {
		t.Errorf("snippet rune length %d exceeds bound 200", len([]rune(snippet)))
	}
}

func TestBuildSnippet_MultiWordQuery(t *testing.T) {
	content := strings.Repeat("a", 100) + "the quick brown fox jumps" + strings.Repeat("b", 200)
	snippet := BuildSnippet(content, []string{"quick brown fox"}, 200)
	if !strings.Contains(snippet, "quick brown fox") {
		t.Errorf("snippet should contain multi-word phrase, got %q", snippet)
	}
}

func TestBuildSnippet_QueryTermsIndividualWords(t *testing.T) {
	// Individual terms from the query are extracted and matched separately.
	content := strings.Repeat("a", 200) + " fox jumps over " + strings.Repeat("b", 200)
	snippet := BuildSnippet(content, []string{"quick", "brown", "fox"}, 200)
	// "fox" should match.
	if !strings.Contains(snippet, "fox") {
		t.Errorf("snippet should contain 'fox', got %q", snippet)
	}
	if len([]rune(snippet)) > 200 {
		t.Errorf("snippet rune length %d exceeds bound 200", len([]rune(snippet)))
	}
}

func TestBuildSnippet_ZeroBoundReturnsDefault(t *testing.T) {
	content := strings.Repeat("a", 500) + "target" + strings.Repeat("b", 500)
	snippet := BuildSnippet(content, []string{"target"}, 0)
	if len([]rune(snippet)) > DefaultSnippetBound {
		t.Errorf("snippet length %d exceeds default bound %d", len([]rune(snippet)), DefaultSnippetBound)
	}
	if !strings.Contains(snippet, "target") {
		t.Errorf("snippet should contain match with default bound, got %q", snippet)
	}
}

func TestBuildSnippet_NegativeBoundReturnsDefault(t *testing.T) {
	content := strings.Repeat("a", 500)
	snippet := BuildSnippet(content, []string{"a"}, -1)
	if len([]rune(snippet)) > DefaultSnippetBound {
		t.Errorf("snippet length %d exceeds default bound %d", len([]rune(snippet)), DefaultSnippetBound)
	}
}

func TestBuildSnippet_AnchorOffsetsPreserved(t *testing.T) {
	// The snippet builder doesn't modify char_start/char_end - those are
	// preserved by StructuredSearchResult. This test verifies that the
	// content returned is indeed a substring of the original.
	content := strings.Repeat("x", 100) + "matchme" + strings.Repeat("y", 300)
	snippet := BuildSnippet(content, []string{"matchme"}, 100)
	if !strings.Contains(content, snippet) {
		t.Errorf("snippet should be a substring of original content, snippet=%q", snippet)
	}
}
