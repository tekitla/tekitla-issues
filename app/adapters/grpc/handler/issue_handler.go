package handler

import (
	"context"

	"go.uber.org/zap"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/mapper"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// IssueHandler implementa issuesv1.IssueServiceServer.
//
// Los handlers son finos: identidad -> request -> service -> mapper ->
// response. Nada de lógica de negocio acá. Todo error sale por
// domainerrors.MapToGRPC.
type IssueHandler struct {
	// Embed obligatorio: forward-compat con RPCs nuevos del proto.
	issuesv1.UnimplementedIssueServiceServer

	logger     *zap.Logger
	projectSvc *application.ProjectService
	issueSvc   *application.IssueService
	statusSvc  *application.StatusService
	labelSvc   *application.LabelService
}

var _ issuesv1.IssueServiceServer = (*IssueHandler)(nil)

// NewIssueHandler construye el handler.
func NewIssueHandler(
	logger *zap.Logger,
	projectSvc *application.ProjectService,
	issueSvc *application.IssueService,
	statusSvc *application.StatusService,
	labelSvc *application.LabelService,
) *IssueHandler {
	return &IssueHandler{logger: logger, projectSvc: projectSvc, issueSvc: issueSvc, statusSvc: statusSvc, labelSvc: labelSvc}
}

func (h *IssueHandler) ListProjects(ctx context.Context, req *issuesv1.ListProjectsRequest) (*issuesv1.ListProjectsResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	page, err := h.projectSvc.ListProjects(ctx, uid, application.ListProjectsInput{
		WorkspaceID: req.GetWorkspaceId(),
		Page:        mapper.PageFilterFromProto(req.GetPage()),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListProjectsResponse{
		Projects: mapper.ProjectsToProto(page.Projects),
		Page:     mapper.PageResponseToProto(page.NextCursor, page.HasNext),
	}, nil
}

func (h *IssueHandler) CreateProject(ctx context.Context, req *issuesv1.CreateProjectRequest) (*issuesv1.CreateProjectResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	project, err := h.projectSvc.CreateProject(ctx, uid, application.CreateProjectInput{
		WorkspaceID: req.GetWorkspaceId(),
		Key:         req.GetKey(),
		Name:        req.GetName(),
		Description: req.GetDescription(),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateProjectResponse{Project: mapper.ProjectToProto(project)}, nil
}

func (h *IssueHandler) ListIssues(ctx context.Context, req *issuesv1.ListIssuesRequest) (*issuesv1.ListIssuesResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	page, err := h.issueSvc.ListIssues(ctx, uid, application.ListIssuesInput{
		ProjectID: req.GetProjectId(),
		State:     mapper.IssueStateFromProto(req.GetState()),
		Cursor:    req.GetPage().GetCursor(),
		Limit:     req.GetPage().GetLimit(),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListIssuesResponse{
		Issues: mapper.IssuesToProto(page.Issues),
		Page:   mapper.PageResponseToProto(page.NextCursor, page.HasNext),
	}, nil
}

func (h *IssueHandler) GetIssue(ctx context.Context, req *issuesv1.GetIssueRequest) (*issuesv1.GetIssueResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	issue, err := h.issueSvc.GetIssue(ctx, uid, req.GetProjectId(), req.GetNumber())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.GetIssueResponse{Issue: mapper.IssueToProto(issue)}, nil
}

func (h *IssueHandler) CreateIssue(ctx context.Context, req *issuesv1.CreateIssueRequest) (*issuesv1.CreateIssueResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	issue, err := h.issueSvc.CreateIssue(ctx, uid, application.CreateIssueInput{
		ProjectID:   req.GetProjectId(),
		Title:       req.GetTitle(),
		Description: req.GetDescription(),
		Priority:    mapper.PriorityFromProto(req.GetPriority()),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateIssueResponse{Issue: mapper.IssueToProto(issue)}, nil
}

func (h *IssueHandler) MoveIssue(ctx context.Context, req *issuesv1.MoveIssueRequest) (*issuesv1.MoveIssueResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	issue, err := h.issueSvc.MoveIssue(ctx, uid, application.MoveIssueInput{
		ProjectID: req.GetProjectId(),
		Number:    req.GetNumber(),
		State:     mapper.IssueStateFromProto(req.GetState()),
		StatusID:  req.GetStatusId(),
		Place:     port.Placement{AfterNumber: req.GetAfterNumber(), BeforeNumber: req.GetBeforeNumber()},
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.MoveIssueResponse{Issue: mapper.IssueToProto(issue)}, nil
}
