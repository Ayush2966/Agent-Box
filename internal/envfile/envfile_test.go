package envfile

import (
	"reflect"
	"testing"
)

func TestRenderRewritesInPlaceAndAppends(t *testing.T) {
	tmpl := "# app settings\nAPP_PORT=3000\n\nexport DATABASE_URL=postgres://x/app # main db\nSECRET=keep-me\n"
	got := string(Render([]byte(tmpl), []KV{
		{"APP_PORT", "20001"},
		{"DATABASE_URL", "postgres://x/app_agentbox_a"},
		{"AGENTBOX_NAME", "a"},
	}))
	want := "# app settings\nAPP_PORT=20001\n\nexport DATABASE_URL=postgres://x/app_agentbox_a\nSECRET=keep-me\n\n# added by agentbox\nAGENTBOX_NAME=a\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderLaterVarWins(t *testing.T) {
	got := string(Render(nil, []KV{{"A", "1"}, {"B", "2"}, {"A", "3"}}))
	want := "# added by agentbox\nA=3\nB=2\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRenderReplacesDuplicateTemplateLines(t *testing.T) {
	got := string(Render([]byte("A=1\nA=2\n"), []KV{{"A", "9"}}))
	if got != "A=9\nA=9\n" {
		t.Fatalf("got %q", got)
	}
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"":            `""`,
		"has space":   "'has space'",
		"p@ss#word":   "'p@ss#word'",
		"it's":        `"it's"`,
		"line\nbreak": `"line\nbreak"`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParse(t *testing.T) {
	got := Parse([]byte("# c\nA=1\nexport B=\"two words\"\nC='x' \nD=val # comment\r\nnot a line\n"))
	want := []KV{{"A", "1"}, {"B", "two words"}, {"C", "x"}, {"D", "val"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPairs(t *testing.T) {
	got := Pairs([]KV{{"A", "1"}, {"B", "x y"}})
	if !reflect.DeepEqual(got, []string{"A=1", "B=x y"}) {
		t.Fatalf("got %v", got)
	}
}
