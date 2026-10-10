package handler_test

import (
	"context"
	"net"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/handler"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// Test de punta a punta del adapter gRPC (sin DB): cliente real -> bufconn ->
// handler -> services -> repos en memoria. Verifica el wiring, la identidad
// por metadata y que los errores de dominio lleguen con el código gRPC correcto.
//
// La lógica se testea en app/application; acá alcanza con el recorrido.

// memStore implementa port.WorkspaceRepository, port.ProjectRepository y
// port.IssueRepository en memoria (sin paginación: devuelve todo en una página).
type memStore struct {
	mu         sync.Mutex
	workspaces map[string]*model.Workspace // por owner_uid
	projects   map[string]*model.Project
	next       map[string]int32
	issues     []*model.Issue
}

func newMemStore() *memStore {
	return &memStore{
		workspaces: map[string]*model.Workspace{},
		projects:   map[string]*model.Project{},
		next:       map[string]int32{},
	}
}

type memWorkspaces struct{ *memStore }
type memProjects struct{ *memStore }
type memIssues struct{ *memStore }

var (
	_ port.WorkspaceRepository = memWorkspaces{}
	_ port.ProjectRepository   = memProjects{}
	_ port.IssueRepository     = memIssues{}
)

func (s memWorkspaces) GetPersonal(_ context.Context, ownerUID string) (*model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ws, ok := s.workspaces[ownerUID]; ok {
		return ws, nil
	}
	return nil, domainerrors.NotFoundError("workspace not found", nil)
}

func (s memWorkspaces) EnsurePersonal(_ context.Context, ws *model.Workspace) (*model.Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.workspaces[ws.OwnerUID]; ok {
		return existing, nil
	}
	s.workspaces[ws.OwnerUID] = ws
	return ws, nil
}

func (s memProjects) Create(_ context.Context, p *model.Project) (*model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.projects {
		if other.WorkspaceID == p.WorkspaceID && other.Key == p.Key {
			return nil, domainerrors.ConflictError("project already exists", nil)
		}
	}
	p.CreatedAt, p.UpdatedAt = time.Now(), time.Now()
	s.projects[p.ID] = p
	s.next[p.ID] = 1
	return p, nil
}

func (s memProjects) GetByID(_ context.Context, workspaceIDs []string, id string) (*model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[id]; ok && slices.Contains(workspaceIDs, p.WorkspaceID) {
		return p, nil
	}
	return nil, domainerrors.NotFoundError("project not found", nil)
}

func (s memProjects) List(_ context.Context, workspaceIDs []string, _ port.PageFilter) (*port.ProjectPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := &port.ProjectPage{}
	for _, p := range s.projects {
		if slices.Contains(workspaceIDs, p.WorkspaceID) {
			page.Projects = append(page.Projects, p)
		}
	}
	sort.Slice(page.Projects, func(i, j int) bool { return page.Projects[i].Name < page.Projects[j].Name })
	return page, nil
}

func (s memIssues) Create(_ context.Context, workspaceIDs []string, issue *model.Issue) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[issue.ProjectID]; !ok || !slices.Contains(workspaceIDs, p.WorkspaceID) {
		return nil, domainerrors.NotFoundError("project not found", nil)
	}
	last := ""
	for _, it := range s.issues {
		if it.ProjectID == issue.ProjectID && it.Rank > last {
			last = it.Rank
		}
	}
	rank, err := model.RankAfter(last)
	if err != nil {
		return nil, err
	}
	issue.Rank = rank
	issue.Number = s.next[issue.ProjectID]
	s.next[issue.ProjectID]++
	issue.CreatedAt, issue.UpdatedAt = time.Now(), time.Now()
	s.issues = append(s.issues, issue)
	return issue, nil
}

func (s memIssues) find(projectID string, number int32) *model.Issue {
	for _, it := range s.issues {
		if it.ProjectID == projectID && it.Number == number {
			return it
		}
	}
	return nil
}

func (s memIssues) GetByNumber(_ context.Context, projectID string, number int32) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.find(projectID, number); it != nil {
		return it, nil
	}
	return nil, domainerrors.NotFoundError("issue not found", nil)
}

func (s memIssues) List(_ context.Context, f port.IssueFilter) (*port.IssuePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := &port.IssuePage{}
	for _, it := range s.issues {
		if it.ProjectID == f.ProjectID && (f.State == "" || it.State == f.State) {
			page.Issues = append(page.Issues, it)
		}
	}
	sort.Slice(page.Issues, func(i, j int) bool { return page.Issues[i].Rank < page.Issues[j].Rank })
	return page, nil
}

func (s memIssues) UpdateState(_ context.Context, projectID string, number int32, state model.IssueState) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.find(projectID, number)
	if it == nil {
		return nil, domainerrors.NotFoundError("issue not found", nil)
	}
	it.State = state
	return it, nil
}

func newClient(t *testing.T) issuesv1.IssueServiceClient {
	t.Helper()

	store := newMemStore()
	workspaces, projects, issues := memWorkspaces{store}, memProjects{store}, memIssues{store}
	h := handler.NewIssueHandler(zap.NewNop(),
		application.NewProjectService(workspaces, projects),
		application.NewIssueService(workspaces, projects, issues),
	)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	issuesv1.RegisterIssueServiceServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return issuesv1.NewIssueServiceClient(conn)
}

// asUser devuelve un context con la metadata que pondría el gateway.
// uid vacío = sin x-user-id.
func asUser(t *testing.T, uid string) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if uid == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "x-user-id", uid, "x-request-id", "req-1")
}

func wantCode(t *testing.T, what string, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Errorf("%s: got %v (%v), want %v", what, got, err, want)
	}
}

func TestIssueHandler_Flow(t *testing.T) {
	client := newClient(t)
	ctx := asUser(t, "u1")

	created, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "tk", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := created.GetProject()
	if project.GetKey() != "TK" || project.GetId() == "" || project.GetWorkspaceId() == "" || project.GetCreatedAt() == nil {
		t.Fatalf("CreateProject: got %+v", project)
	}

	for i, p := range []issuesv1.Priority{issuesv1.Priority_PRIORITY_UNSPECIFIED, issuesv1.Priority_PRIORITY_URGENT, issuesv1.Priority_PRIORITY_LOW} {
		res, err := client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: project.GetId(), Title: "issue", Priority: p})
		if err != nil {
			t.Fatalf("CreateIssue %d: %v", i, err)
		}
		if res.GetIssue().GetNumber() != int32(i+1) || res.GetIssue().GetState() != issuesv1.IssueState_ISSUE_STATE_TODO {
			t.Errorf("CreateIssue %d: got %+v", i, res.GetIssue())
		}
		if i == 0 && res.GetIssue().GetPriority() != issuesv1.Priority_PRIORITY_MEDIUM {
			t.Errorf("default priority: got %v, want MEDIUM", res.GetIssue().GetPriority())
		}
	}

	list, err := client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: project.GetId()})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	// Orden por rank: cada issue nuevo va al final, sin importar la prioridad.
	var numbers []int32
	prevRank := ""
	for _, it := range list.GetIssues() {
		numbers = append(numbers, it.GetNumber())
		if it.GetRank() <= prevRank {
			t.Errorf("issue %d: rank %q not after %q", it.GetNumber(), it.GetRank(), prevRank)
		}
		prevRank = it.GetRank()
	}
	if !slices.Equal(numbers, []int32{1, 2, 3}) {
		t.Errorf("ListIssues order: got %v, want [1 2 3]", numbers)
	}

	moved, err := client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: project.GetId(), Number: 2, State: issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS})
	if err != nil {
		t.Fatalf("MoveIssue: %v", err)
	}
	if moved.GetIssue().GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS {
		t.Errorf("MoveIssue: got %v", moved.GetIssue().GetState())
	}

	got, err := client.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: project.GetId(), Number: 2})
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.GetIssue().GetState() != issuesv1.IssueState_ISSUE_STATE_IN_PROGRESS {
		t.Errorf("GetIssue: got %+v", got.GetIssue())
	}

	filtered, err := client.ListIssues(ctx, &issuesv1.ListIssuesRequest{ProjectId: project.GetId(), State: issuesv1.IssueState_ISSUE_STATE_TODO})
	if err != nil || len(filtered.GetIssues()) != 2 {
		t.Errorf("ListIssues TODO: got %d issues, err %v", len(filtered.GetIssues()), err)
	}

	canceled, err := client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: project.GetId(), Number: 3, State: issuesv1.IssueState_ISSUE_STATE_CANCELED})
	if err != nil || canceled.GetIssue().GetState() != issuesv1.IssueState_ISSUE_STATE_CANCELED {
		t.Errorf("MoveIssue CANCELED: got %+v, err %v", canceled.GetIssue(), err)
	}

	projects, err := client.ListProjects(ctx, &issuesv1.ListProjectsRequest{})
	if err != nil || len(projects.GetProjects()) != 1 || projects.GetPage().GetHasNext() {
		t.Errorf("ListProjects: got %+v, err %v", projects, err)
	}
	ws := project.GetWorkspaceId()
	inWorkspace, err := client.ListProjects(ctx, &issuesv1.ListProjectsRequest{WorkspaceId: ws})
	if err != nil || len(inWorkspace.GetProjects()) != 1 || inWorkspace.GetProjects()[0].GetWorkspaceId() != ws {
		t.Errorf("ListProjects workspace_id propio: got %+v, err %v", inWorkspace, err)
	}
	second, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{WorkspaceId: ws, Key: "OT", Name: "Otro"})
	if err != nil || second.GetProject().GetWorkspaceId() != ws {
		t.Errorf("CreateProject en workspace_id propio: got %+v, err %v", second.GetProject(), err)
	}
}

func TestIssueHandler_ErrorCodes(t *testing.T) {
	client := newClient(t)
	ctx := asUser(t, "u1")

	created, err := client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Tekitla"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pid := created.GetProject().GetId()

	// Sin identidad: UNAUTHENTICATED en todos los RPCs.
	anon := asUser(t, "")
	_, err = client.ListProjects(anon, &issuesv1.ListProjectsRequest{})
	wantCode(t, "ListProjects sin x-user-id", err, codes.Unauthenticated)
	_, err = client.CreateProject(anon, &issuesv1.CreateProjectRequest{Key: "AB", Name: "x"})
	wantCode(t, "CreateProject sin x-user-id", err, codes.Unauthenticated)
	_, err = client.GetIssue(anon, &issuesv1.GetIssueRequest{ProjectId: pid, Number: 1})
	wantCode(t, "GetIssue sin x-user-id", err, codes.Unauthenticated)
	_, err = client.ListIssues(asUser(t, "   "), &issuesv1.ListIssuesRequest{ProjectId: pid})
	wantCode(t, "x-user-id en blanco", err, codes.Unauthenticated)

	// Proyecto de otro espacio: NOT_FOUND (no PERMISSION_DENIED).
	other := asUser(t, "u2")
	_, err = client.ListIssues(other, &issuesv1.ListIssuesRequest{ProjectId: pid})
	wantCode(t, "ListIssues ajeno", err, codes.NotFound)
	_, err = client.CreateIssue(other, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: "x"})
	wantCode(t, "CreateIssue ajeno", err, codes.NotFound)
	if res, err := client.ListProjects(other, &issuesv1.ListProjectsRequest{}); err != nil || len(res.GetProjects()) != 0 {
		t.Errorf("ListProjects de u2: got %+v, err %v", res, err)
	}
	// Espacio de otro usuario: lista vacía al listar, NOT_FOUND al crear.
	ws := created.GetProject().GetWorkspaceId()
	if res, err := client.ListProjects(other, &issuesv1.ListProjectsRequest{WorkspaceId: ws}); err != nil || len(res.GetProjects()) != 0 {
		t.Errorf("ListProjects de u2 en el espacio de u1: got %+v, err %v", res, err)
	}
	_, err = client.CreateProject(other, &issuesv1.CreateProjectRequest{WorkspaceId: ws, Key: "XX", Name: "x"})
	wantCode(t, "CreateProject en espacio ajeno", err, codes.NotFound)
	// La misma key en otro espacio sí se puede.
	if _, err := client.CreateProject(other, &issuesv1.CreateProjectRequest{Key: "TK", Name: "Otro"}); err != nil {
		t.Errorf("CreateProject TK de u2: %v", err)
	}

	// Duplicado y validaciones.
	_, err = client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "tk", Name: "Otra vez"})
	wantCode(t, "key duplicada", err, codes.AlreadyExists)
	_, err = client.CreateProject(ctx, &issuesv1.CreateProjectRequest{Key: "1X", Name: "x"})
	wantCode(t, "key inválida", err, codes.InvalidArgument)
	_, err = client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 1})
	wantCode(t, "MoveIssue UNSPECIFIED", err, codes.InvalidArgument)
	_, err = client.MoveIssue(ctx, &issuesv1.MoveIssueRequest{ProjectId: pid, Number: 1, State: issuesv1.IssueState(42)})
	wantCode(t, "MoveIssue estado desconocido", err, codes.InvalidArgument)
	_, err = client.CreateIssue(ctx, &issuesv1.CreateIssueRequest{ProjectId: pid, Title: " "})
	wantCode(t, "título vacío", err, codes.InvalidArgument)
	_, err = client.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: pid, Number: 99})
	wantCode(t, "issue inexistente", err, codes.NotFound)
	_, err = client.GetIssue(ctx, &issuesv1.GetIssueRequest{ProjectId: "no-uuid", Number: 1})
	wantCode(t, "project_id inválido", err, codes.InvalidArgument)
}
