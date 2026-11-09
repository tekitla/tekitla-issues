// Entry point de issues-service: solo wiring + ciclo de vida.
// Nada de lógica acá.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"

	"github.com/tekitla/tekitla-issues/app/adapters/grpc/handler"
	"github.com/tekitla/tekitla-issues/app/adapters/grpc/interceptor"
	"github.com/tekitla/tekitla-issues/app/application"
	"github.com/tekitla/tekitla-issues/app/infra/bootstrap"
	"github.com/tekitla/tekitla-issues/app/infra/postgres"
	"github.com/tekitla/tekitla-issues/app/infra/telemetry"
)

func main() {
	// `<binario> healthcheck`: HEALTHCHECK de docker compose (ver healthcheck.go).
	if len(os.Args) > 1 && os.Args[1] == healthcheckCmd {
		os.Exit(runHealthcheck(5000))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Infraestructura
	infra := bootstrap.Init(ctx)
	defer infra.Shutdown()

	// 2. Banner
	telemetry.PrintBanner(infra.Config, infra.Logger)

	// 3. Repositorios
	workspaceRepo := postgres.NewWorkspaceRepository(infra.DB)
	projectRepo := postgres.NewProjectRepository(infra.DB)
	issueRepo := postgres.NewIssueRepository(infra.DB)
	statusRepo := postgres.NewStatusRepository(infra.DB)
	labelRepo := postgres.NewLabelRepository(infra.DB)
	actorRepo := postgres.NewActorRepository(infra.DB)
	tokenRepo := postgres.NewTokenRepository(infra.DB)

	// 4. Servicios de aplicación
	projectSvc := application.NewProjectService(workspaceRepo, projectRepo)
	issueSvc := application.NewIssueService(workspaceRepo, projectRepo, issueRepo, statusRepo, labelRepo, actorRepo)
	statusSvc := application.NewStatusService(workspaceRepo, projectRepo, statusRepo, actorRepo)
	labelSvc := application.NewLabelService(workspaceRepo, projectRepo, labelRepo)
	actorSvc := application.NewActorService(actorRepo, tokenRepo)

	// 5. Handlers gRPC
	issueHandler := handler.NewIssueHandler(infra.Logger, projectSvc, issueSvc, statusSvc, labelSvc)
	actorHandler := handler.NewActorHandler(infra.Logger, actorSvc)
	tokenAuthHandler := handler.NewTokenAuthHandler(infra.Logger, actorSvc)

	// 6. Servidor gRPC
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(interceptor.UnaryLogger(infra.Logger, infra.Config.Debug)),
	)
	issuesv1.RegisterIssueServiceServer(grpcServer, issueHandler)
	issuesv1.RegisterActorServiceServer(grpcServer, actorHandler)
	// Interno: lo llama solo el gateway (el servicio no tiene puerto publicado).
	issuesv1.RegisterTokenAuthServiceServer(grpcServer, tokenAuthHandler)

	// Health: lo usan los probes de Cloud Run y el healthcheck de docker compose.
	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthSrv)
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Reflection solo fuera de production (grpcurl en local/QA).
	if infra.Config.AppEnv != "production" {
		reflection.Register(grpcServer)
	}

	// 7. Listener
	addr := fmt.Sprintf(":%d", infra.Config.GRPCPort)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		infra.Logger.Fatal("failed to listen", zap.String("addr", addr), zap.Error(err))
	}

	// 8. Servir en goroutine
	go func() {
		infra.Logger.Info("gRPC server listening", zap.String("addr", addr))
		if err := grpcServer.Serve(lis); err != nil {
			infra.Logger.Error("gRPC server stopped", zap.Error(err))
		}
	}()

	// 9. Esperar señal de shutdown (Cloud Run y docker mandan SIGTERM)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	infra.Logger.Info("shutdown signal received, draining...")
	healthSrv.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)

	// 10. Graceful shutdown con timeout de 10s
	stopDone := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		infra.Logger.Info("gRPC server stopped gracefully")
	case <-time.After(10 * time.Second):
		infra.Logger.Warn("shutdown timeout, forcing stop")
		grpcServer.Stop()
	}
}
