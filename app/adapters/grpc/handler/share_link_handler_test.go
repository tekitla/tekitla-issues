package handler_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Repo en memoria de los enlaces del issue 009. El lock del proyecto y el
// índice único los cubren el repo de Postgres y make test-migrations.

type memShareLink struct {
	link *model.ShareLink
	hash []byte
}

type memShareLinks struct{ *memStore }

var _ port.ShareLinkRepository = memShareLinks{}

func (s memShareLinks) Create(_ context.Context, link *model.ShareLink, hash []byte, maxActive int) (*model.ShareLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, l := range s.shareLinks {
		if l.link.ProjectID == link.ProjectID && l.link.IsActive(link.CreatedAt) {
			active++
		}
	}
	if active >= maxActive {
		return nil, domainerrors.FailedPreconditionError("limit", nil).WithReason("SHARE_LINK_LIMIT")
	}
	s.shareLinks = append(s.shareLinks, &memShareLink{link: link, hash: hash})
	return link, nil
}

func (s memShareLinks) List(_ context.Context, projectID string) ([]*model.ShareLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*model.ShareLink
	for i := len(s.shareLinks) - 1; i >= 0; i-- {
		if l := s.shareLinks[i].link; l.ProjectID == projectID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s memShareLinks) get(projectID, id string) *model.ShareLink {
	for _, l := range s.shareLinks {
		if l.link.ProjectID == projectID && l.link.ID == id {
			return l.link
		}
	}
	return nil
}

func (s memShareLinks) Revoke(_ context.Context, projectID, id string, at time.Time) (*model.ShareLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.get(projectID, id)
	if l == nil {
		return nil, domainerrors.NotFoundError("share link not found", nil)
	}
	if l.RevokedAt == nil {
		l.RevokedAt = &at
	}
	return l, nil
}

func (s memShareLinks) Resolve(_ context.Context, hash []byte, now time.Time) (*model.ShareLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.shareLinks {
		if bytes.Equal(l.hash, hash) && l.link.IsActive(now) {
			l.link.LastUsedAt = &now
			return l.link, nil
		}
	}
	return nil, domainerrors.NotFoundError("share link not found", nil)
}

// anon es un context sin identidad: lo que manda el gateway en una ruta
// pública sin credenciales.
func anon(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// shareFixture arma un proyecto de u1 con issues inventados: uno con «Qué
// espera» asignado a la persona, uno asignado a un agente, uno hecho y uno
// descartado.
type shareFixture struct {
	c         clients
	projectID string
	canceled  int32
}

func newShareFixture(t *testing.T) shareFixture {
	t.Helper()
	c := newClients(t)
	ctx := asUser(t, "u1")
	p, err := c.issues.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "DEMO", Name: "Demo", Description: "Proyecto de prueba"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	projectID := p.GetProject().GetId()
	actors, err := c.actors.ListActors(ctx, &issuesv1.ListActorsRequest{})
	if err != nil {
		t.Fatalf("ListActors: %v", err)
	}
	person := actors.GetActors()[0].GetId()
	agent, err := c.actors.CreateAgent(ctx, &issuesv1.CreateAgentRequest{DisplayName: "Codex PC-01", Tool: issuesv1.AgentTool_AGENT_TOOL_CODEX})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	create := func(title, desc, assignee string) int32 {
		res, err := c.issues.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: projectID, Title: title, Description: desc, AssigneeActorId: assignee})
		if err != nil {
			t.Fatalf("CreateIssue %q: %v", title, err)
		}
		return res.GetIssue().GetNumber()
	}
	create("Revisar el aviso", "> **Espera:** ana\n> **Qué espera:** que apruebe el **texto**\n\n## Descripción\n\nDetalle interno.", person)
	create("Pantalla de alta", "Detalle del agente.", agent.GetAgent().GetId())
	done := create("Hecho", "", "")
	if _, err := c.issues.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: projectID, Number: done, State: issuesv1.IssueState_ISSUE_STATE_DONE}); err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	canceled := create("Descartado", "", "")
	if _, err := c.issues.DiscardIssue(ctx, &issuesv1.DiscardIssueRequest{ProjectId: projectID, Number: canceled, Reason: "motivo interno"}); err != nil {
		t.Fatalf("DiscardIssue: %v", err)
	}
	return shareFixture{c: c, projectID: projectID, canceled: canceled}
}

func (f shareFixture) create(t *testing.T, scope issuesv1.ShareLinkScope) *issuesv1.CreateShareLinkResponse {
	t.Helper()
	res, err := f.c.share.CreateShareLink(asUser(t, "u1"), &issuesv1.CreateShareLinkRequest{
		ProjectId: f.projectID, Scope: scope, Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_THIRTY_DAYS,
		Note: "  jefe  ", OwnerDisplayName: "Victor",
	})
	if err != nil {
		t.Fatalf("CreateShareLink: %v", err)
	}
	return res
}

func TestShareLink_Management(t *testing.T) {
	f := newShareFixture(t)
	ctx := asUser(t, "u1")

	res := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN)
	link, token := res.GetShareLink(), res.GetToken()
	if !strings.HasPrefix(token, "tks_") || len(token) != 47 {
		t.Fatalf("token: got %q", token)
	}
	if link.GetNote() != "jefe" || link.GetScope() != issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN ||
		link.GetOwnerDisplayName() != "Victor" || link.GetExpiresAt() == nil || link.GetRevokedAt() != nil {
		t.Fatalf("link: got %+v", link)
	}
	if d := link.GetExpiresAt().AsTime().Sub(link.GetCreatedAt().AsTime()); d != 30*24*time.Hour {
		t.Errorf("vencimiento: got %v, want 30 días", d)
	}
	// Se guarda solo el hash.
	sum := sha256.Sum256([]byte(token))
	stored := f.c.store.shareLinks[0]
	if !bytes.Equal(stored.hash, sum[:]) || stored.link.CreatedByUID != "u1" {
		t.Errorf("guardado: hash %x, created_by %q", stored.hash, stored.link.CreatedByUID)
	}

	// Sin vencimiento.
	never, err := f.c.share.CreateShareLink(ctx, &issuesv1.CreateShareLinkRequest{
		ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL,
		Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_NEVER, Note: "compañero",
	})
	if err != nil || never.GetShareLink().GetExpiresAt() != nil {
		t.Fatalf("sin vencimiento: %v %+v", err, never.GetShareLink())
	}

	list, err := f.c.share.ListShareLinks(ctx, &issuesv1.ListShareLinksRequest{ProjectId: f.projectID})
	if err != nil || len(list.GetShareLinks()) != 2 || list.GetShareLinks()[0].GetNote() != "compañero" {
		t.Fatalf("ListShareLinks: %v %+v", err, list.GetShareLinks())
	}
	if raw, _ := protojson.Marshal(list); bytes.Contains(raw, []byte("tks_")) || bytes.Contains(raw, []byte("u1")) {
		t.Errorf("la lista no lleva el token ni el uid: %s", raw)
	}

	// Revocar: idempotente.
	for i := 0; i < 2; i++ {
		rev, err := f.c.share.RevokeShareLink(ctx, &issuesv1.RevokeShareLinkRequest{ProjectId: f.projectID, ShareLinkId: link.GetId()})
		if err != nil || rev.GetShareLink().GetRevokedAt() == nil {
			t.Fatalf("RevokeShareLink %d: %v", i, err)
		}
	}

	// Validaciones.
	bad := []*issuesv1.CreateShareLinkRequest{
		{ProjectId: f.projectID, Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS, Note: "x"},
		{ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN, Note: "x"},
		{ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN, Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS, Note: "   "},
		{ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN, Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS, Note: strings.Repeat("n", 81)},
		{ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN, Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS, Note: "x", OwnerDisplayName: strings.Repeat("v", 41)},
	}
	for i, req := range bad {
		_, err := f.c.share.CreateShareLink(ctx, req)
		wantCode(t, "validación "+string(rune('0'+i)), err, codes.InvalidArgument)
	}
	_, err = f.c.share.RevokeShareLink(ctx, &issuesv1.RevokeShareLinkRequest{ProjectId: f.projectID, ShareLinkId: "no-uuid"})
	wantCode(t, "revocar id inválido", err, codes.InvalidArgument)
	_, err = f.c.share.RevokeShareLink(ctx, &issuesv1.RevokeShareLinkRequest{ProjectId: f.projectID, ShareLinkId: "00000000-0000-4000-8000-000000000000"})
	wantCode(t, "revocar inexistente", err, codes.NotFound)
}

func TestShareLink_OnlyTheOwner(t *testing.T) {
	f := newShareFixture(t)
	link := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL).GetShareLink()

	// Otra persona: el proyecto no existe para ella.
	other := asUser(t, "u2")
	_, err := f.c.share.CreateShareLink(other, &issuesv1.CreateShareLinkRequest{
		ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN,
		Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS, Note: "x",
	})
	wantCode(t, "crear ajeno", err, codes.NotFound)
	_, err = f.c.share.ListShareLinks(other, &issuesv1.ListShareLinksRequest{ProjectId: f.projectID})
	wantCode(t, "listar ajeno", err, codes.NotFound)
	_, err = f.c.share.RevokeShareLink(other, &issuesv1.RevokeShareLinkRequest{ProjectId: f.projectID, ShareLinkId: link.GetId()})
	wantCode(t, "revocar ajeno", err, codes.NotFound)

	// Sin identidad y con un agente.
	_, err = f.c.share.ListShareLinks(anon(t), &issuesv1.ListShareLinksRequest{ProjectId: f.projectID})
	wantCode(t, "sin identidad", err, codes.Unauthenticated)
	agentCtx := metadata.AppendToOutgoingContext(asUser(t, "u1"), "x-agent-id", "00000000-0000-4000-8000-000000000001")
	_, err = f.c.share.ListShareLinks(agentCtx, &issuesv1.ListShareLinksRequest{ProjectId: f.projectID})
	wantCode(t, "agente", err, codes.PermissionDenied)
}

func TestShareLink_Limit(t *testing.T) {
	f := newShareFixture(t)
	for i := 0; i < model.MaxActiveShareLinksPerProject; i++ {
		f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN)
	}
	_, err := f.c.share.CreateShareLink(asUser(t, "u1"), &issuesv1.CreateShareLinkRequest{
		ProjectId: f.projectID, Scope: issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN,
		Duration: issuesv1.ShareLinkDuration_SHARE_LINK_DURATION_SEVEN_DAYS, Note: "uno más",
	})
	wantCode(t, "tope", err, codes.FailedPrecondition)
	if errorReason(err) != "SHARE_LINK_LIMIT" {
		t.Errorf("reason: got %q", errorReason(err))
	}
}

// CU-009-6: alcance plan, sin descripción, con «Qué espera», sin issues
// sueltos.
func TestSharedProject_PlanScope(t *testing.T) {
	f := newShareFixture(t)
	token := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN).GetToken()
	ctx := anon(t)

	project, err := f.c.shared.GetSharedProject(ctx, &issuesv1.GetSharedProjectRequest{Token: token})
	if err != nil {
		t.Fatalf("GetSharedProject: %v", err)
	}
	if project.GetProject().GetKey() != "DEMO" || project.GetScope() != issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN {
		t.Fatalf("proyecto: got %+v", project)
	}
	for _, st := range project.GetStatuses() {
		if st.GetCategory() == issuesv1.IssueState_ISSUE_STATE_CANCELED {
			t.Errorf("estado descartado en la vista pública: %+v", st)
		}
	}
	if len(project.GetStatuses()) != 3 {
		t.Errorf("estados: got %d, want 3", len(project.GetStatuses()))
	}

	list, err := f.c.shared.ListSharedIssues(ctx, &issuesv1.ListSharedIssuesRequest{Token: token})
	if err != nil {
		t.Fatalf("ListSharedIssues: %v", err)
	}
	issues := list.GetIssues()
	if len(issues) != 3 {
		t.Fatalf("issues: got %d, want 3 (sin el descartado)", len(issues))
	}
	first := issues[0]
	if first.GetDescription() != "" || first.GetWaitingNote() != "que apruebe el texto" {
		t.Errorf("plan: description %q, waiting_note %q", first.GetDescription(), first.GetWaitingNote())
	}
	if a := first.GetAssignee(); a.GetKind() != issuesv1.ActorKind_ACTOR_KIND_PERSON || a.GetDisplayName() != "Victor" {
		t.Errorf("persona: got %+v", a)
	}
	if a := issues[1].GetAssignee(); a.GetKind() != issuesv1.ActorKind_ACTOR_KIND_AGENT ||
		a.GetDisplayName() != "Codex PC-01" || a.GetTool() != issuesv1.AgentTool_AGENT_TOOL_CODEX {
		t.Errorf("agente: got %+v", a)
	}
	if issues[2].GetAssignee() != nil {
		t.Errorf("sin asignar: got %+v", issues[2].GetAssignee())
	}

	_, err = f.c.shared.GetSharedIssue(ctx, &issuesv1.GetSharedIssueRequest{Token: token, Number: first.GetNumber()})
	wantCode(t, "issue con alcance plan", err, codes.NotFound)
}

// CU-009-7: alcance panel, con descripción y detalle de issues.
func TestSharedProject_PanelScope(t *testing.T) {
	f := newShareFixture(t)
	token := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL).GetToken()
	ctx := anon(t)

	list, err := f.c.shared.ListSharedIssues(ctx, &issuesv1.ListSharedIssuesRequest{Token: token})
	if err != nil {
		t.Fatalf("ListSharedIssues: %v", err)
	}
	if first := list.GetIssues()[0]; !strings.Contains(first.GetDescription(), "Detalle interno") || first.GetWaitingNote() != "" {
		t.Errorf("panel: description %q, waiting_note %q", first.GetDescription(), first.GetWaitingNote())
	}
	got, err := f.c.shared.GetSharedIssue(ctx, &issuesv1.GetSharedIssueRequest{Token: token, Number: 2})
	if err != nil || got.GetIssue().GetTitle() != "Pantalla de alta" {
		t.Fatalf("GetSharedIssue: %v %+v", err, got.GetIssue())
	}
	for _, n := range []int32{f.canceled, 99, 0} {
		_, err := f.c.shared.GetSharedIssue(ctx, &issuesv1.GetSharedIssueRequest{Token: token, Number: n})
		wantCode(t, "issue descartado, inexistente o 0", err, codes.NotFound)
	}

	// Con identidad (sesión abierta, otra persona) la respuesta es la misma.
	withUser, err := f.c.shared.ListSharedIssues(asUser(t, "u2"), &issuesv1.ListSharedIssuesRequest{Token: token})
	if err != nil || !proto.Equal(withUser, list) {
		t.Errorf("con identidad: %v", err)
	}
}

// CU-009-2 y CU-009-8: mal formado, desconocido, vencido o revocado dan el
// mismo NOT_FOUND; revocar uno no toca a los demás.
func TestSharedProject_InvalidTokens(t *testing.T) {
	f := newShareFixture(t)
	revoked := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL)
	expired := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL)
	alive := f.create(t, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL)
	ctx := anon(t)

	if _, err := f.c.share.RevokeShareLink(asUser(t, "u1"), &issuesv1.RevokeShareLinkRequest{ProjectId: f.projectID, ShareLinkId: revoked.GetShareLink().GetId()}); err != nil {
		t.Fatalf("RevokeShareLink: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	for _, l := range f.c.store.shareLinks {
		if l.link.ID == expired.GetShareLink().GetId() {
			l.link.ExpiresAt = &past
		}
	}

	tokens := map[string]string{
		"vacío":       "",
		"sin prefijo": strings.TrimPrefix(alive.GetToken(), "tks_"),
		"tka_":        "tka_" + strings.TrimPrefix(alive.GetToken(), "tks_"),
		"desconocido": "tks_" + strings.Repeat("A", 43),
		"revocado":    revoked.GetToken(),
		"vencido":     expired.GetToken(),
	}
	var messages []string
	for name, token := range tokens {
		_, err := f.c.shared.GetSharedProject(ctx, &issuesv1.GetSharedProjectRequest{Token: token})
		wantCode(t, name, err, codes.NotFound)
		messages = append(messages, status.Convert(err).Message())
		_, err = f.c.shared.ListSharedIssues(ctx, &issuesv1.ListSharedIssuesRequest{Token: token})
		wantCode(t, name+" (lista)", err, codes.NotFound)
	}
	if uniq := slices.Compact(slices.Sorted(slices.Values(messages))); len(uniq) != 1 {
		t.Errorf("mensajes distintos: %q", messages)
	}

	if _, err := f.c.shared.GetSharedProject(ctx, &issuesv1.GetSharedProjectRequest{Token: alive.GetToken()}); err != nil {
		t.Errorf("el enlace vigente dejó de andar: %v", err)
	}
	for _, l := range f.c.store.shareLinks {
		if l.link.ID == alive.GetShareLink().GetId() && l.link.LastUsedAt == nil {
			t.Error("last_used_at no se marcó")
		}
	}
}

// CU-009-4: las respuestas públicas no llevan emails, uids ni ids internos.
func TestSharedProject_NoPrivateData(t *testing.T) {
	f := newShareFixture(t)
	ctx := anon(t)
	for _, scope := range []issuesv1.ShareLinkScope{issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PLAN, issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL} {
		token := f.create(t, scope).GetToken()
		project, err := f.c.shared.GetSharedProject(ctx, &issuesv1.GetSharedProjectRequest{Token: token})
		if err != nil {
			t.Fatalf("GetSharedProject: %v", err)
		}
		list, err := f.c.shared.ListSharedIssues(ctx, &issuesv1.ListSharedIssuesRequest{Token: token})
		if err != nil {
			t.Fatalf("ListSharedIssues: %v", err)
		}
		msgs := []proto.Message{project, list}
		if scope == issuesv1.ShareLinkScope_SHARE_LINK_SCOPE_PANEL {
			one, err := f.c.shared.GetSharedIssue(ctx, &issuesv1.GetSharedIssueRequest{Token: token, Number: 1})
			if err != nil {
				t.Fatalf("GetSharedIssue: %v", err)
			}
			msgs = append(msgs, one)
		}
		forbidden := []string{"u1", "@", f.projectID, "motivo interno"}
		for _, a := range f.c.store.actors {
			forbidden = append(forbidden, a.ID)
		}
		for _, it := range f.c.store.issues {
			forbidden = append(forbidden, it.ID)
		}
		for _, m := range msgs {
			raw, err := protojson.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			for _, bad := range forbidden {
				if bytes.Contains(raw, []byte(bad)) {
					t.Errorf("%v: la respuesta lleva %q: %s", scope, bad, raw)
				}
			}
		}
	}
}

// La lista cerrada de campos de las respuestas públicas (plan del 009, §4): un
// campo nuevo en un mensaje Shared* tiene que agregarse acá a propósito.
func TestSharedMessages_ClosedFieldList(t *testing.T) {
	want := map[string][]string{
		"SharedProject":            {"key", "name", "description"},
		"SharedStatus":             {"id", "name", "category", "color", "wip_limit", "hidden"},
		"SharedLabel":              {"id", "name", "color"},
		"SharedActor":              {"kind", "display_name", "tool"},
		"SharedIssue":              {"number", "title", "description", "waiting_note", "state", "priority", "status_id", "label_ids", "assignee", "created_at", "updated_at"},
		"GetSharedProjectResponse": {"project", "scope", "statuses", "labels"},
		"ListSharedIssuesResponse": {"issues", "page"},
		"GetSharedIssueResponse":   {"issue"},
	}
	msgs := issuesv1.File_issues_v1_share_links_proto.Messages()
	for name, fields := range want {
		md := msgs.ByName(protoreflect.Name(name))
		if md == nil {
			t.Errorf("%s no existe", name)
			continue
		}
		var got []string
		for i := 0; i < md.Fields().Len(); i++ {
			got = append(got, string(md.Fields().Get(i).Name()))
		}
		if !slices.Equal(got, fields) {
			t.Errorf("%s: campos %v, want %v", name, got, fields)
		}
	}
}
