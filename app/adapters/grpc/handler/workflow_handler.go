package handler

import (
	"context"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/mapper"
	"github.com/tekitla/tekitla-issues/app/application"
	domainerrors "github.com/tekitla/tekitla-issues/app/domain/errors"
	"github.com/tekitla/tekitla-issues/app/domain/model"
	"github.com/tekitla/tekitla-issues/app/domain/port"
)

// RPCs de la rebanada 3a: edición de issues, estados, etiquetas, comentarios
// e historial. Mismo patrón que issue_handler.go.

func (h *IssueHandler) UpdateIssue(ctx context.Context, req *issuesv1.UpdateIssueRequest) (*issuesv1.UpdateIssueResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	in := application.UpdateIssueInput{
		ProjectID:   req.GetProjectId(),
		Number:      req.GetNumber(),
		Title:       req.Title,
		Description: req.Description,
	}
	if req.Priority != nil {
		p := mapper.PriorityFromProto(req.GetPriority())
		in.Priority = &p
	}
	if req.Labels != nil {
		ids := append([]string{}, req.GetLabels().GetIds()...)
		in.LabelIDs = &ids
	}
	issue, err := h.issueSvc.UpdateIssue(ctx, uid, in)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.UpdateIssueResponse{Issue: mapper.IssueToProto(issue)}, nil
}

func (h *IssueHandler) ListStatuses(ctx context.Context, req *issuesv1.ListStatusesRequest) (*issuesv1.ListStatusesResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	statuses, err := h.statusSvc.ListStatuses(ctx, uid, req.GetProjectId())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListStatusesResponse{Statuses: mapper.StatusesToProto(statuses)}, nil
}

func (h *IssueHandler) CreateStatus(ctx context.Context, req *issuesv1.CreateStatusRequest) (*issuesv1.CreateStatusResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	status, err := h.statusSvc.CreateStatus(ctx, uid, application.CreateStatusInput{
		ProjectID:     req.GetProjectId(),
		Name:          req.GetName(),
		Category:      mapper.IssueStateFromProto(req.GetCategory()),
		AfterStatusID: req.GetAfterStatusId(),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateStatusResponse{Status: mapper.StatusToProto(status)}, nil
}

func (h *IssueHandler) UpdateStatus(ctx context.Context, req *issuesv1.UpdateStatusRequest) (*issuesv1.UpdateStatusResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	status, err := h.statusSvc.UpdateStatus(ctx, uid, application.UpdateStatusInput{
		ProjectID:     req.GetProjectId(),
		StatusID:      req.GetStatusId(),
		Name:          req.Name,
		AfterStatusID: req.AfterStatusId,
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.UpdateStatusResponse{Status: mapper.StatusToProto(status)}, nil
}

func (h *IssueHandler) DeleteStatus(ctx context.Context, req *issuesv1.DeleteStatusRequest) (*issuesv1.DeleteStatusResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	if err := h.statusSvc.DeleteStatus(ctx, uid, req.GetProjectId(), req.GetStatusId(), req.GetMoveToStatusId()); err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.DeleteStatusResponse{}, nil
}

func (h *IssueHandler) ListLabels(ctx context.Context, req *issuesv1.ListLabelsRequest) (*issuesv1.ListLabelsResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	labels, err := h.labelSvc.ListLabels(ctx, uid, req.GetProjectId())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.ListLabelsResponse{Labels: mapper.LabelsToProto(labels)}, nil
}

func (h *IssueHandler) CreateLabel(ctx context.Context, req *issuesv1.CreateLabelRequest) (*issuesv1.CreateLabelResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	label, err := h.labelSvc.CreateLabel(ctx, uid, req.GetProjectId(), req.GetName(), mapper.LabelColorFromProto(req.GetColor()))
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateLabelResponse{Label: mapper.LabelToProto(label)}, nil
}

func (h *IssueHandler) UpdateLabel(ctx context.Context, req *issuesv1.UpdateLabelRequest) (*issuesv1.UpdateLabelResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	var color *model.LabelColor
	if req.Color != nil {
		c := mapper.LabelColorFromProto(req.GetColor())
		color = &c
	}
	label, err := h.labelSvc.UpdateLabel(ctx, uid, req.GetProjectId(), req.GetLabelId(), req.Name, color)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.UpdateLabelResponse{Label: mapper.LabelToProto(label)}, nil
}

func (h *IssueHandler) DeleteLabel(ctx context.Context, req *issuesv1.DeleteLabelRequest) (*issuesv1.DeleteLabelResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	if err := h.labelSvc.DeleteLabel(ctx, uid, req.GetProjectId(), req.GetLabelId()); err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.DeleteLabelResponse{}, nil
}

func (h *IssueHandler) CreateComment(ctx context.Context, req *issuesv1.CreateCommentRequest) (*issuesv1.CreateCommentResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	comment, err := h.issueSvc.CreateComment(ctx, uid, req.GetProjectId(), req.GetNumber(), req.GetBody())
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	return &issuesv1.CreateCommentResponse{Comment: mapper.CommentToProto(comment)}, nil
}

func (h *IssueHandler) ListIssueEvents(ctx context.Context, req *issuesv1.ListIssueEventsRequest) (*issuesv1.ListIssueEventsResponse, error) {
	uid, err := requireUserID(ctx)
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	page, err := h.issueSvc.ListIssueEvents(ctx, uid, req.GetProjectId(), req.GetNumber(), port.PageFilter{
		Cursor: req.GetPage().GetCursor(),
		Limit:  req.GetPage().GetLimit(),
	})
	if err != nil {
		return nil, domainerrors.MapToGRPC(err, h.logger)
	}
	events, err := mapper.IssueEventsToProto(page.Events)
	if err != nil {
		return nil, domainerrors.MapToGRPC(domainerrors.InternalError("failed to map issue events", map[string]any{"cause": err.Error()}), h.logger)
	}
	return &issuesv1.ListIssueEventsResponse{
		Events: events,
		Page:   mapper.PageResponseToProto(page.NextCursor, page.HasNext),
	}, nil
}
