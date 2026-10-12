package model_test

import (
	"strconv"
	"strings"
	"testing"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
)

func TestParseIssueSearch(t *testing.T) {
	cases := []struct {
		raw  string
		want model.IssueSearch
	}{
		{"", model.IssueSearch{}},
		{"   ", model.IssueSearch{}},
		{" login ", model.IssueSearch{Text: "login"}},
		{"12", model.IssueSearch{Text: "12", Number: 12}},
		{"TK-12", model.IssueSearch{Text: "TK-12", Number: 12}},
		{"tk-7", model.IssueSearch{Text: "tk-7", Number: 7}},
		{"OT-7", model.IssueSearch{Text: "OT-7"}},               // otra key: solo título
		{"TK-", model.IssueSearch{Text: "TK-"}},                 // sin número
		{"0", model.IssueSearch{Text: "0"}},                     // los números empiezan en 1
		{"12a", model.IssueSearch{Text: "12a"}},                 // no es solo dígitos
		{"99999999999", model.IssueSearch{Text: "99999999999"}}, // no cabe en int32
	}
	for _, c := range cases {
		got, err := model.ParseIssueSearch(c.raw, "TK")
		if err != nil || got != c.want {
			t.Errorf("ParseIssueSearch(%q): got %+v, %v; want %+v", c.raw, got, err, c.want)
		}
	}
	if _, err := model.ParseIssueSearch(strings.Repeat("ñ", 200), "TK"); err != nil {
		t.Errorf("200 runas: %v", err)
	}
	if _, err := model.ParseIssueSearch(strings.Repeat("a", 201), "TK"); !domainerrors.IsValidation(err) {
		t.Errorf("201 runas: expected ValidationError, got %v", err)
	}
}

func TestStatusDefaults(t *testing.T) {
	want := map[model.IssueState]model.LabelColor{
		model.IssueStateTodo: model.LabelColorNeutral, model.IssueStateInProgress: model.LabelColorInfo,
		model.IssueStateDone: model.LabelColorSuccess, model.IssueStateCanceled: model.LabelColorDanger,
	}
	for category, color := range want {
		if got := model.DefaultStatusColor(category); got != color {
			t.Errorf("DefaultStatusColor(%s): got %d, want %d", category, got, color)
		}
		if got, _ := model.ResolveStatusColor(model.LabelColorUnspecified, category); got != color {
			t.Errorf("ResolveStatusColor(unspecified, %s): got %d", category, got)
		}
		if hidden := model.DefaultStatusHidden(category); hidden != (category == model.IssueStateCanceled) {
			t.Errorf("DefaultStatusHidden(%s): got %v", category, hidden)
		}
	}
	if got, err := model.ResolveStatusColor(model.LabelColorBrand, model.IssueStateTodo); err != nil || got != model.LabelColorBrand {
		t.Errorf("ResolveStatusColor(brand): got %d, %v", got, err)
	}
	if _, err := model.ResolveStatusColor(model.LabelColor(9), model.IssueStateTodo); !domainerrors.IsValidation(err) {
		t.Errorf("ResolveStatusColor(9): expected ValidationError, got %v", err)
	}
	if err := model.ValidateWIPLimit(-1); !domainerrors.IsValidation(err) {
		t.Errorf("ValidateWIPLimit(-1): expected ValidationError, got %v", err)
	}
}

func TestNormalizeDiscardReason(t *testing.T) {
	if got, err := model.NormalizeDiscardReason("  duplicado  "); err != nil || got != "duplicado" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, raw := range []string{"", "   ", strings.Repeat("x", 501)} {
		if _, err := model.NormalizeDiscardReason(raw); !domainerrors.IsValidation(err) {
			t.Errorf("NormalizeDiscardReason(%d runas): expected ValidationError, got %v", len(raw), err)
		}
	}
}

// --- PlanStatusBatch ---

func batchCurrent() []*model.Status {
	return []*model.Status{
		{ID: "todo", Name: "Por hacer", Category: model.IssueStateTodo},
		{ID: "doing", Name: "En curso", Category: model.IssueStateInProgress},
		{ID: "done", Name: "Hecho", Category: model.IssueStateDone},
		{ID: "canceled", Name: "Descartado", Category: model.IssueStateCanceled},
	}
}

func keepAll() []model.StatusSpec {
	var specs []model.StatusSpec
	for _, s := range batchCurrent() {
		specs = append(specs, model.StatusSpec{ID: s.ID, Name: s.Name})
	}
	return specs
}

func newIDs() func() string {
	n := 0
	return func() string { n++; return "new" + strconv.Itoa(n) }
}

func TestPlanStatusBatch_OK(t *testing.T) {
	specs := []model.StatusSpec{
		{ID: "doing", Name: " Haciendo ", WIPLimit: 3},
		{ID: "todo", Name: "Por hacer", Color: model.LabelColorBrand},
		{Name: "Revisión", Category: model.IssueStateInProgress},
		{ID: "done", Name: "en curso"}, // toma el nombre que deja libre «doing»
		{ID: "canceled", Name: "Descartado", Hidden: true},
	}
	plan, err := model.PlanStatusBatch(batchCurrent(), specs, nil, newIDs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got []string
	for _, s := range plan.Final {
		got = append(got, s.ID+":"+s.Name)
	}
	if strings.Join(got, ",") != "doing:Haciendo,todo:Por hacer,new1:Revisión,done:en curso,canceled:Descartado" {
		t.Errorf("final: %v", got)
	}
	if !plan.IsNew["new1"] || plan.IsNew["todo"] {
		t.Errorf("IsNew: %v", plan.IsNew)
	}
	f := plan.Final
	if f[0].WIPLimit != 3 || f[0].Color != model.LabelColorInfo || f[1].Color != model.LabelColorBrand ||
		f[2].Category != model.IssueStateInProgress || !f[4].Hidden {
		t.Errorf("campos: %+v %+v %+v %+v", f[0], f[1], f[2], f[4])
	}
}

func TestPlanStatusBatch_Deletion(t *testing.T) {
	specs := keepAll()[1:] // sin «todo»: falta su categoría
	_, err := model.PlanStatusBatch(batchCurrent(), specs, []model.StatusDeletion{{StatusID: "todo"}}, newIDs())
	if !domainerrors.IsFailedPrecondition(err) || reasonOf(err) != "LAST_STATUS_OF_CATEGORY" {
		t.Errorf("categoría vacía: got %v", err)
	}

	specs = append(keepAll(), model.StatusSpec{Name: "Backlog", Category: model.IssueStateTodo})
	specs = specs[1:]
	plan, err := model.PlanStatusBatch(batchCurrent(), specs, []model.StatusDeletion{{StatusID: "todo", MoveToStatusID: "doing"}}, newIDs())
	if err != nil || len(plan.Deletions) != 1 || len(plan.Final) != 4 {
		t.Fatalf("borrar con destino: got %+v, %v", plan, err)
	}
}

func TestPlanStatusBatch_Errors(t *testing.T) {
	cases := map[string]struct {
		specs     []model.StatusSpec
		deletions []model.StatusDeletion
		check     func(error) bool
		reason    string
	}{
		"falta un estado (vista vieja)": {specs: keepAll()[:3], check: domainerrors.IsFailedPrecondition, reason: "STALE_STATUSES"},
		"id ajeno":                      {specs: append(keepAll(), model.StatusSpec{ID: "otro", Name: "X"}), check: domainerrors.IsValidation},
		"id repetido":                   {specs: append(keepAll(), model.StatusSpec{ID: "todo", Name: "X"}), check: domainerrors.IsValidation},
		"cambia la categoría": {specs: func() []model.StatusSpec {
			s := keepAll()
			s[0].Category = model.IssueStateDone
			return s
		}(), check: domainerrors.IsValidation},
		"nuevo sin categoría":  {specs: append(keepAll(), model.StatusSpec{Name: "X"}), check: domainerrors.IsValidation},
		"nombre vacío":         {specs: append(keepAll(), model.StatusSpec{Name: " ", Category: model.IssueStateTodo}), check: domainerrors.IsValidation},
		"wip negativo":         {specs: append(keepAll(), model.StatusSpec{Name: "X", Category: model.IssueStateTodo, WIPLimit: -1}), check: domainerrors.IsValidation},
		"color inválido":       {specs: append(keepAll(), model.StatusSpec{Name: "X", Category: model.IssueStateTodo, Color: 42}), check: domainerrors.IsValidation},
		"nombre repetido":      {specs: append(keepAll(), model.StatusSpec{Name: "HECHO", Category: model.IssueStateDone}), check: domainerrors.IsConflict, reason: "STATUS_NAME_TAKEN"},
		"queda y se borra":     {specs: keepAll(), deletions: []model.StatusDeletion{{StatusID: "todo"}}, check: domainerrors.IsValidation},
		"borrar ajeno":         {specs: keepAll(), deletions: []model.StatusDeletion{{StatusID: "otro"}}, check: domainerrors.IsValidation},
		"destino que no queda": {specs: keepAll()[1:], deletions: []model.StatusDeletion{{StatusID: "todo", MoveToStatusID: "todo"}}, check: domainerrors.IsValidation},
		"destino nuevo":        {specs: append(keepAll()[1:], model.StatusSpec{Name: "B", Category: model.IssueStateTodo}), deletions: []model.StatusDeletion{{StatusID: "todo", MoveToStatusID: "new1"}}, check: domainerrors.IsValidation},
		"más del máximo": {specs: func() []model.StatusSpec {
			s := keepAll()
			for i := 0; i < model.MaxStatusesPerProject; i++ {
				s = append(s, model.StatusSpec{Name: "E" + strconv.Itoa(i), Category: model.IssueStateTodo})
			}
			return s
		}(), check: domainerrors.IsFailedPrecondition},
	}
	for name, c := range cases {
		_, err := model.PlanStatusBatch(batchCurrent(), c.specs, c.deletions, newIDs())
		if !c.check(err) {
			t.Errorf("%s: unexpected error %v", name, err)
			continue
		}
		if c.reason != "" && reasonOf(err) != c.reason {
			t.Errorf("%s: reason %q, want %q", name, reasonOf(err), c.reason)
		}
	}
}

func reasonOf(err error) string {
	if de, ok := err.(*domainerrors.DomainError); ok {
		return de.Reason
	}
	return ""
}
