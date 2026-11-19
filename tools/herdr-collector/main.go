// herdr-collector lee las sesiones de agentes de herdr en esta máquina y las
// reporta a Tekitla con ReportSessions (issue 026). Corre en la máquina de la
// persona, al lado de herdr, y nunca bloquea a los agentes: si herdr o
// Tekitla no responden, anota el error y sigue.
//
//	TEKITLA_TOKEN=tka_… go run ./tools/herdr-collector -url https://api.tekitla.example \
//	    -root C:\dev -machine PC-01 -projects round=RND,tekitla=TK
//	go run ./tools/herdr-collector -dry-run -once -root C:\dev -projects tekitla=TK   # solo muestra el reporte
//
// Cada -interval (15 s) corre `herdr agent list` y `herdr workspace list`;
// manda la lista completa cuando algo cambia, y si no, cada -heartbeat (2
// min) como latido. El token (de la persona, con el permiso sessions:write)
// sale de la variable de entorno TEKITLA_TOKEN, nunca de un archivo.
//
// Datos personales: cwd viaja relativo a -root ("" fuera de ella). No viajan
// rutas absolutas ni el usuario del sistema.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

const (
	reportPath = "/v1/agentSessions:report"
	// maxSessions es el tope de ReportSessionsRequest.sessions.
	maxSessions = 200
)

func main() {
	var (
		url       = flag.String("url", "", "URL del gateway de Tekitla (sin barra al final)")
		root      = flag.String("root", "", `raíz de los repos, p. ej. C:\dev`)
		machine   = flag.String("machine", "", "nombre corto de esta máquina ([A-Za-z0-9._-], hasta 40)")
		projects  = flag.String("projects", "", "mapa de proyectos: nombre=CLAVE separados por coma, p. ej. round=RND,tekitla=TK")
		interval  = flag.Duration("interval", 15*time.Second, "cada cuánto se lee herdr")
		heartbeat = flag.Duration("heartbeat", 2*time.Minute, "latido: se manda aunque no cambie nada")
		herdrBin  = flag.String("herdr", "herdr", "binario de herdr")
		once      = flag.Bool("once", false, "un solo ciclo y termina")
		dryRun    = flag.Bool("dry-run", false, "no manda nada: muestra el reporte")
	)
	flag.Parse()
	logger := log.New(os.Stderr, "herdr-collector ", log.LstdFlags)

	projectMap, err := parseProjects(*projects)
	if err != nil {
		logger.Fatal(err)
	}
	cfg := config{root: *root, projects: projectMap}
	if *machine == "" && !*dryRun {
		logger.Fatal("falta -machine")
	}
	var send sender
	if *dryRun {
		send = printSender(os.Stdout)
	} else {
		token := os.Getenv("TEKITLA_TOKEN")
		if *url == "" || token == "" {
			logger.Fatal("faltan -url o TEKITLA_TOKEN")
		}
		send = httpSender(*url, token, &http.Client{Timeout: 10 * time.Second})
	}

	c := &collector{cfg: cfg, machine: *machine, read: herdrReader(*herdrBin), send: send, heartbeat: *heartbeat, logger: logger, now: time.Now}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c.cycle(ctx)
	if *once {
		return
	}
	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.cycle(ctx)
		}
	}
}

// reader lee herdr: los agentes y las etiquetas de los workspaces.
type reader func(ctx context.Context) ([]herdrAgent, map[string]string, error)

// sender manda un reporte.
type sender func(ctx context.Context, req *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error)

type collector struct {
	cfg       config
	machine   string
	read      reader
	send      sender
	heartbeat time.Duration
	logger    *log.Logger
	now       func() time.Time

	last     []byte    // último reporte mandado con éxito
	lastSent time.Time // cuándo
}

// cycle lee, traduce y manda si cambió algo o toca latido. Nunca termina el
// proceso: los errores se anotan y se reintenta en el siguiente ciclo
// (CU-026-10).
func (c *collector) cycle(ctx context.Context) {
	agents, workspaces, err := c.read(ctx)
	if err != nil {
		c.logger.Printf("no se pudo leer herdr (se reintenta): %v", err)
		return
	}
	sessions := translate(c.cfg, agents, workspaces)
	if len(sessions) > maxSessions {
		// Más que el tope del contrato haría rechazar el reporte entero (y el
		// latido): se mandan las primeras.
		c.logger.Printf("herdr tiene %d sesiones: se mandan %d (tope de ReportSessions)", len(sessions), maxSessions)
		sessions = sessions[:maxSessions]
	}
	req := &issuesv1.ReportSessionsRequest{
		Source:   issuesv1.SessionSource_SESSION_SOURCE_HERDR,
		Machine:  c.machine,
		Sessions: sessions,
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		c.logger.Printf("no se pudo armar el reporte: %v", err)
		return
	}
	now := c.now()
	if bytes.Equal(b, c.last) && now.Sub(c.lastSent) < c.heartbeat {
		return
	}
	res, err := c.send(ctx, req)
	if err != nil {
		c.logger.Printf("no se pudo mandar el reporte (se reintenta): %v", err)
		return
	}
	c.last, c.lastSent = b, now
	c.logger.Printf("reporte: %d sesiones; nuevas %d, cambiadas %d, sin cambios %d, terminadas %d",
		len(req.GetSessions()), res.GetCreated(), res.GetUpdated(), res.GetUnchanged(), res.GetEnded())
	for _, rj := range res.GetRejected() {
		c.logger.Printf("sesión %s: %s", rj.GetExternalId(), rj.GetReason())
	}
}

// herdrReader corre la CLI de herdr (con 10 s de tope por comando).
func herdrReader(bin string) reader {
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
	return func(ctx context.Context) ([]herdrAgent, map[string]string, error) {
		a, err := run(ctx, "agent", "list")
		if err != nil {
			return nil, nil, err
		}
		agents, err := parseAgents(a)
		if err != nil {
			return nil, nil, err
		}
		w, err := run(ctx, "workspace", "list")
		if err != nil {
			return nil, nil, err
		}
		workspaces, err := parseWorkspaces(w)
		if err != nil {
			return nil, nil, err
		}
		return agents, workspaces, nil
	}
}

// httpSender manda el reporte al gateway por REST con el token.
func httpSender(baseURL, token string, client *http.Client) sender {
	return func(ctx context.Context, req *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
		body, err := protojson.Marshal(req)
		if err != nil {
			return nil, err
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+reportPath, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		hreq.Header.Set("Authorization", "Bearer "+token)
		hreq.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(hreq)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			// El cuerpo de error del gateway no trae el token.
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		var out issuesv1.ReportSessionsResponse
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("respuesta inválida: %w", err)
		}
		return &out, nil
	}
}

// printSender muestra el reporte (-dry-run) y responde vacío.
func printSender(w io.Writer) sender {
	return func(_ context.Context, req *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
		b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(req)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintln(w, string(b)); err != nil {
			return nil, err
		}
		return &issuesv1.ReportSessionsResponse{}, nil
	}
}
