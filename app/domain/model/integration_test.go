package model

import (
	"slices"
	"testing"
)

func TestBranchIssueRefs(t *testing.T) {
	cases := []struct {
		branch string
		want   []int32
	}{
		{"feat/tk-9-login", []int32{9}},        // CU-016-3
		{"FEAT/TK-9-login", []int32{9}},        // sin distinguir mayúsculas
		{"tk-9", []int32{9}},                   // al principio
		{"feat/issue-231-login", []int32{231}}, // convención de Round y Tekitla
		{"feat/issue-16-repos-github", []int32{16}},
		{"fix/typo", nil},                   // CU-016-6
		{"feat/tk-9-tk-10", []int32{9, 10}}, // dos issues
		{"feat/atk-9", nil},                 // otra clave que termina igual
		{"feat/tk-09", nil},                 // sin ceros a la izquierda
		{"feat/tk-0", nil},
		{"feat/tk-91", []int32{91}},       // no corta el número
		{"feat/rnd-9", nil},               // clave de otro proyecto
		{"feat/tk-9-issue-9", []int32{9}}, // sin repetidos
		{"feat/myissue-3", nil},           // issue- con letra antes
	}
	for _, c := range cases {
		if got := BranchIssueRefs("TK", c.branch); !slices.Equal(got, c.want) {
			t.Errorf("BranchIssueRefs(%q) = %v, want %v", c.branch, got, c.want)
		}
	}
}

func TestTextIssueRefs(t *testing.T) {
	cases := []struct {
		text string
		want []int32
	}{
		{"Login con Google (TK-9)", []int32{9}},
		{"Cierra tk-9 y TK-12.", []int32{9, 12}},
		{"Ver issue-9", nil}, // issue-<n> solo vale en la rama
		{"RND-4", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := TextIssueRefs("TK", c.text); !slices.Equal(got, c.want) {
			t.Errorf("TextIssueRefs(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestAggregateCI(t *testing.T) {
	cases := []struct {
		suites map[string]CIStatus
		want   CIStatus
	}{
		{nil, CIStatusNone},
		{map[string]CIStatus{"1": CIStatusSuccess}, CIStatusSuccess},
		{map[string]CIStatus{"1": CIStatusSuccess, "2": CIStatusPending}, CIStatusPending},
		{map[string]CIStatus{"1": CIStatusPending, "2": CIStatusFailed}, CIStatusFailed},
		{map[string]CIStatus{"1": CIStatusSuccess, "2": CIStatusCanceled}, CIStatusCanceled},
	}
	for _, c := range cases {
		if got := AggregateCI(c.suites); got != c.want {
			t.Errorf("AggregateCI(%v) = %v, want %v", c.suites, got, c.want)
		}
	}
}

func TestSelectRepos(t *testing.T) {
	remote := []RemoteRepo{
		{ExternalID: 1, Name: "web", Topics: []string{"tekitla"}},
		{ExternalID: 2, Name: "api", Topics: []string{"Tekitla", "go"}},
		{ExternalID: 3, Name: "sitio", Topics: []string{"personal"}},
		{ExternalID: 4, Name: "viejo", Topics: []string{"tekitla"}, Archived: true},
	}
	names := func(rs []RemoteRepo) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return out
	}
	// CU-016-1: solo los del topic, sin archivados, por nombre.
	if got := names(SelectRepos(remote, ConnectionScopeTopic, "tekitla", nil)); !slices.Equal(got, []string{"api", "web"}) {
		t.Errorf("topic: got %v", got)
	}
	if got := names(SelectRepos(remote, ConnectionScopeSelected, "", []string{"SITIO"})); !slices.Equal(got, []string{"sitio"}) {
		t.Errorf("selected: got %v", got)
	}
	if got := names(SelectRepos(remote, ConnectionScopeAll, "", nil)); len(got) != 3 {
		t.Errorf("all: got %v", got)
	}
}

func TestNormalizeScope(t *testing.T) {
	if _, _, err := NormalizeScope(ConnectionScopeUnspecified, "", nil); err == nil {
		t.Error("unspecified: want error")
	}
	if topic, _, err := NormalizeScope(ConnectionScopeTopic, " Tekitla ", nil); err != nil || topic != "tekitla" {
		t.Errorf("topic: got %q, %v", topic, err)
	}
	if _, _, err := NormalizeScope(ConnectionScopeTopic, "con espacio", nil); err == nil {
		t.Error("bad topic: want error")
	}
	if _, sel, err := NormalizeScope(ConnectionScopeSelected, "", []string{"api", "API", "web"}); err != nil || len(sel) != 2 {
		t.Errorf("selected: got %v, %v", sel, err)
	}
	if _, _, err := NormalizeScope(ConnectionScopeSelected, "", []string{"a,b"}); err == nil {
		t.Error("comma in name: want error")
	}
}

func TestFirstLineAndBranch(t *testing.T) {
	if got := FirstLine("feat: login\n\ncuerpo largo", HeadTitleMaxLen); got != "feat: login" {
		t.Errorf("FirstLine: %q", got)
	}
	long := ""
	for range 200 {
		long += "ñ"
	}
	if got := FirstLine(long, HeadTitleMaxLen); len([]rune(got)) != HeadTitleMaxLen {
		t.Errorf("FirstLine no recorta por runas: %d", len([]rune(got)))
	}
	if ValidBranch("") || ValidBranch("a\nb") || !ValidBranch("feat/tk-9-login") {
		t.Error("ValidBranch")
	}
}
