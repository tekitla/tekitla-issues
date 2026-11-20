package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

var testCfg = config{root: `C:\dev`, projects: map[string]string{"round": "RND", "tekitla": "TK", "dev-plantillas-wt": "DP"}}

func agent(ext, name, status, cwd, ws string, tokens map[string]string) herdrAgent {
	a := herdrAgent{Agent: "claude", Name: name, AgentStatus: status, Cwd: cwd, WorkspaceID: ws, Tokens: tokens}
	a.AgentSession.Value = ext
	return a
}

func TestParseHerdr(t *testing.T) {
	agents, err := parseAgents([]byte(`{"id":"1","result":{"agents":[{"agent":"claude","agent_status":"blocked",
	  "agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"abc"},"cwd":"C:\\dev\\round",
	  "name":"round-issue-231","workspace_id":"w1","state_labels":{"idle":"libre"},"tokens":{"espera":"🙋 pregunta","espera_tipo":"persona"}}]}}`))
	if err != nil || len(agents) != 1 || agents[0].AgentSession.Value != "abc" || agents[0].Tokens["espera_tipo"] != "persona" || agents[0].Name != "round-issue-231" {
		t.Fatalf("parseAgents: %+v, %v", agents, err)
	}
	ws, err := parseWorkspaces([]byte(`{"id":"2","result":{"type":"workspace_list","workspaces":[{"workspace_id":"w1","label":"round #231","pane_count":1}]}}`))
	if err != nil || ws["w1"] != "round #231" {
		t.Fatalf("parseWorkspaces: %v, %v", ws, err)
	}
	if _, err := parseAgents([]byte("not json")); err == nil {
		t.Error("parseAgents: want error")
	}
}

func TestTranslate(t *testing.T) {
	agents := []herdrAgent{
		// CU-026-1: por el nombre del agente.
		agent("s1", "round-issue-231", "working", `C:\dev\round\worktrees\issue-231\front`, "w1", nil),
		// CU-026-2: sin convención, por el cwd.
		agent("s2", "", "idle", `C:\dev\tekitla\worktrees\issue-25\tekitla-protos`, "w2", nil),
		// CU-026-3: en la raíz, sin issue.
		agent("s3", "", "idle", `C:\dev`, "w3", nil),
		// CU-026-11: blocked + espera_tipo manda sobre el texto.
		agent("s4", "", "blocked", `C:\Users\victor\Desktop`, "w4", map[string]string{"espera_tipo": "sesion", "espera_a": "tk-worktrees", "espera": "🙋 texto engañoso"}),
		// CU-026-12: trabajando con tokens viejos.
		agent("s5", "", "working", `C:\dev\x`, "w3", map[string]string{"espera_tipo": "persona", "espera": "🙋"}),
		// CU-026-13: respaldo por el prefijo.
		agent("s6", "", "blocked", "", "w3", map[string]string{"espera": "⏳ espera a Round"}),
		// Workspace con convención; repo -wt; proyecto fuera del mapa.
		agent("s7", "", "idle", `C:\dev\otro\worktrees\issue-9`, "w5", nil),
		agent("s8", "", "unknown", `c:\DEV\dev-plantillas-wt\issue-12\base`, "w3", nil),
		agent("s9", "otro-issue-3", "done", `C:\dev\otro\worktrees\issue-3`, "w3", nil),
		agent("", "sin-id", "working", "", "w3", nil),
	}
	ws := map[string]string{"w1": "round #231", "w2": "tekitla", "w3": "Plan", "w4": "tekitla", "w5": "tekitla #26"}
	got := map[string]*issuesv1.ReportedSession{}
	for _, s := range translate(testCfg, agents, ws) {
		got[s.GetExternalId()] = s
	}
	if len(got) != 9 {
		t.Fatalf("sessions: got %d, want 9 (sin id se saltea)", len(got))
	}
	type want struct {
		key    string
		n      int32
		link   issuesv1.SessionLink
		cwd    string
		status issuesv1.SessionStatus
		wait   issuesv1.WaitingOn
		target string
	}
	const (
		cwdL  = issuesv1.SessionLink_SESSION_LINK_CWD
		noneL = issuesv1.SessionLink_SESSION_LINK_NONE
		none  = issuesv1.WaitingOn_WAITING_ON_NONE
	)
	cases := map[string]want{
		"s1": {"RND", 231, issuesv1.SessionLink_SESSION_LINK_AGENT_NAME, "round/worktrees/issue-231/front", issuesv1.SessionStatus_SESSION_STATUS_WORKING, none, ""},
		"s2": {"TK", 25, cwdL, "tekitla/worktrees/issue-25/tekitla-protos", issuesv1.SessionStatus_SESSION_STATUS_IDLE, none, ""},
		"s3": {"", 0, noneL, ".", issuesv1.SessionStatus_SESSION_STATUS_IDLE, none, ""},
		"s4": {"", 0, noneL, "", issuesv1.SessionStatus_SESSION_STATUS_WAITING_OK, issuesv1.WaitingOn_WAITING_ON_SESSION, "tk-worktrees"},
		"s5": {"", 0, noneL, "x", issuesv1.SessionStatus_SESSION_STATUS_WORKING, none, ""},
		"s6": {"", 0, noneL, "", issuesv1.SessionStatus_SESSION_STATUS_WAITING_OK, issuesv1.WaitingOn_WAITING_ON_SESSION, "Round"},
		"s7": {"TK", 26, issuesv1.SessionLink_SESSION_LINK_WORKSPACE, "otro/worktrees/issue-9", issuesv1.SessionStatus_SESSION_STATUS_IDLE, none, ""},
		"s8": {"DP", 12, cwdL, "dev-plantillas-wt/issue-12/base", issuesv1.SessionStatus_SESSION_STATUS_UNKNOWN, none, ""},
		// done → en reposo: el panel sigue abierto (decisión de Victor).
		"s9": {"", 0, noneL, "otro/worktrees/issue-3", issuesv1.SessionStatus_SESSION_STATUS_IDLE, none, ""},
	}
	for ext, w := range cases {
		s := got[ext]
		if s.GetProjectKey() != w.key || s.GetIssueNumber() != w.n || s.GetLink() != w.link || s.GetCwdRel() != w.cwd ||
			s.GetStatus() != w.status || s.GetWaitingOn() != w.wait || s.GetWaitingTarget() != w.target {
			t.Errorf("%s: got %+v, want %+v", ext, s, w)
		}
	}
	// CU-026-9: ninguna ruta absoluta ni el usuario del sistema.
	for _, s := range got {
		if strings.Contains(s.GetCwdRel(), ":") || strings.Contains(strings.ToLower(s.GetCwdRel()), "users") || strings.HasPrefix(s.GetCwdRel(), "/") {
			t.Errorf("%s: cwd_rel %q", s.GetExternalId(), s.GetCwdRel())
		}
	}
	if got["s5"].GetWaitingText() != "" {
		t.Error("s5: working must drop waiting_text")
	}
}

// Un panel con textos largos no hace rechazar el reporte: se recortan a los
// límites del contrato (el reporte es también el latido de la máquina).
func TestTranslateClipsLongFields(t *testing.T) {
	long := strings.Repeat("ñ", 300)
	a := agent("s1", long, "blocked", `C:\dev\`+strings.Repeat("a", 600), "w1",
		map[string]string{"espera_tipo": "sesion", "espera_a": long, "espera": long})
	tooLongID := agent(strings.Repeat("x", 201), "", "idle", "", "w1", nil)
	got := translate(testCfg, []herdrAgent{a, tooLongID}, map[string]string{"w1": long})
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1 (id too long skipped)", len(got))
	}
	s := got[0]
	runes := func(x string) int { return len([]rune(x)) }
	if runes(s.GetSessionName()) != 200 || runes(s.GetWorkspaceLabel()) != 200 || runes(s.GetWaitingText()) != 200 ||
		runes(s.GetWaitingTarget()) != 80 || s.GetCwdRel() != "" {
		t.Errorf("clipped: name %d, ws %d, text %d, target %d, cwd %q", runes(s.GetSessionName()), runes(s.GetWorkspaceLabel()),
			runes(s.GetWaitingText()), runes(s.GetWaitingTarget()), s.GetCwdRel())
	}
}

// done con espera: la sesión que terminó su turno y pregunta sigue viva y
// espera a su persona (antes salía terminada y sin espera).
func TestDoneKeepsWaiting(t *testing.T) {
	a := agent("s1", "tekitla-issue-26", "done", "", "w1", map[string]string{"espera_tipo": "persona", "espera": "🙋 decisión"})
	s := translate(testCfg, []herdrAgent{a}, nil)[0]
	if s.GetStatus() != issuesv1.SessionStatus_SESSION_STATUS_IDLE || s.GetWaitingOn() != issuesv1.WaitingOn_WAITING_ON_PERSON || s.GetWaitingText() != "🙋 decisión" {
		t.Errorf("done con espera: %+v", s)
	}
}

// Issue 036: el motivo sale de espera_motivo, solo si la sesión espera.
func TestWaitingReason(t *testing.T) {
	agents := []herdrAgent{
		agent("r1", "", "idle", "", "w1", map[string]string{"espera_tipo": "persona", "espera_motivo": "revision"}),
		agent("r2", "", "blocked", "", "w1", map[string]string{"espera_tipo": "otro", "espera_a": "CI", "espera_motivo": "Deploy"}),
		agent("r3", "", "idle", "", "w1", map[string]string{"espera_tipo": "persona", "espera_motivo": "cafe"}),
		// Trabajando con tokens viejos: sin espera ni motivo.
		agent("r4", "", "working", "", "w1", map[string]string{"espera_tipo": "persona", "espera_motivo": "pruebas"}),
		// Motivo sin espera: no cuenta.
		agent("r5", "", "idle", "", "w1", map[string]string{"espera_motivo": "decision"}),
	}
	want := map[string]issuesv1.WaitingReason{
		"r1": issuesv1.WaitingReason_WAITING_REASON_REVIEW,
		"r2": issuesv1.WaitingReason_WAITING_REASON_DEPLOY,
		"r3": issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED,
		"r4": issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED,
		"r5": issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED,
	}
	for _, s := range translate(testCfg, agents, nil) {
		if s.GetWaitingReason() != want[s.GetExternalId()] {
			t.Errorf("%s: got %v, want %v", s.GetExternalId(), s.GetWaitingReason(), want[s.GetExternalId()])
		}
	}
	if got := reasonOf("pruebas"); got != issuesv1.WaitingReason_WAITING_REASON_TESTING {
		t.Errorf("pruebas: %v", got)
	}
	if got := reasonOf("decision"); got != issuesv1.WaitingReason_WAITING_REASON_DECISION {
		t.Errorf("decision: %v", got)
	}
}

func TestWaitingOfFallback(t *testing.T) {
	cases := map[string]issuesv1.WaitingOn{
		"🙋 decisión":       issuesv1.WaitingOn_WAITING_ON_PERSON,
		"⏳ espera a Round": issuesv1.WaitingOn_WAITING_ON_SESSION,
		"CI de protos":     issuesv1.WaitingOn_WAITING_ON_OTHER,
		"":                 issuesv1.WaitingOn_WAITING_ON_NONE,
	}
	for text, want := range cases {
		if got, _ := waitingOf(map[string]string{"espera": text}); got != want {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
	if got, target := waitingOf(map[string]string{"espera_tipo": "otro", "espera_a": "CI"}); got != issuesv1.WaitingOn_WAITING_ON_OTHER || target != "CI" {
		t.Errorf("otro: %v %q", got, target)
	}
}

func TestParseProjects(t *testing.T) {
	m, err := parseProjects(" Round=rnd , tekitla=TK,")
	if err != nil || m["round"] != "RND" || m["tekitla"] != "TK" || len(m) != 2 {
		t.Fatalf("parseProjects: %v, %v", m, err)
	}
	if _, err := parseProjects("round"); err == nil {
		t.Error("want error")
	}
}

func newTestCollector(read reader, send sender, clock *time.Time) (*collector, *bytes.Buffer) {
	var logs bytes.Buffer
	return &collector{
		cfg: testCfg, machine: "PC-01", read: read, send: send, heartbeat: 2 * time.Minute,
		logger: log.New(&logs, "", 0), now: func() time.Time { return *clock },
	}, &logs
}

func TestCycle_ChangeAndHeartbeat(t *testing.T) {
	clock := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	status := "working"
	read := func(context.Context) ([]herdrAgent, map[string]string, error) {
		return []herdrAgent{agent("s1", "tekitla-issue-26", status, "", "w1", nil)}, map[string]string{}, nil
	}
	sent := 0
	send := func(_ context.Context, req *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
		sent++
		return &issuesv1.ReportSessionsResponse{Rejected: []*issuesv1.SessionRejection{{ExternalId: "s1", Reason: "proyecto TK desconocido"}}}, nil
	}
	c, logs := newTestCollector(read, send, &clock)
	ctx := context.Background()
	c.cycle(ctx) // primero: manda
	c.cycle(ctx) // igual: no manda
	clock = clock.Add(time.Minute)
	status = "blocked"
	c.cycle(ctx) // cambió: manda
	clock = clock.Add(time.Minute)
	c.cycle(ctx) // igual, 1 min: no manda
	clock = clock.Add(90 * time.Second)
	c.cycle(ctx) // latido
	if sent != 3 {
		t.Errorf("sent %d, want 3", sent)
	}
	if !strings.Contains(logs.String(), "proyecto TK desconocido") {
		t.Errorf("rejections not logged: %s", logs.String())
	}
}

func TestCycle_CapsSessions(t *testing.T) {
	clock := time.Now()
	read := func(context.Context) ([]herdrAgent, map[string]string, error) {
		var agents []herdrAgent
		for i := 0; i < maxSessions+5; i++ {
			agents = append(agents, agent("s"+strconv.Itoa(i), "", "idle", "", "w1", nil))
		}
		return agents, nil, nil
	}
	var got int
	send := func(_ context.Context, req *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
		got = len(req.GetSessions())
		return &issuesv1.ReportSessionsResponse{}, nil
	}
	c, logs := newTestCollector(read, send, &clock)
	c.cycle(context.Background())
	if got != maxSessions || !strings.Contains(logs.String(), "tope") {
		t.Errorf("sent %d sessions, logs %q", got, logs.String())
	}
}

// CU-026-10: herdr apagado → anota, no manda, reintenta en el siguiente.
func TestCycle_HerdrDown(t *testing.T) {
	clock := time.Now()
	up := false
	read := func(context.Context) ([]herdrAgent, map[string]string, error) {
		if !up {
			return nil, nil, errors.New("herdr: server not running")
		}
		return nil, nil, nil
	}
	sent := 0
	send := func(context.Context, *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
		sent++
		return &issuesv1.ReportSessionsResponse{}, nil
	}
	c, logs := newTestCollector(read, send, &clock)
	c.cycle(context.Background())
	if sent != 0 || !strings.Contains(logs.String(), "no se pudo leer herdr") {
		t.Errorf("herdr down: sent %d, logs %q", sent, logs.String())
	}
	up = true
	c.cycle(context.Background())
	if sent != 1 {
		t.Errorf("after herdr is back: sent %d", sent)
	}
}

// Tekitla caído: no se marca como mandado y se reintenta en el siguiente.
func TestCycle_SendFailsRetries(t *testing.T) {
	clock := time.Now()
	read := func(context.Context) ([]herdrAgent, map[string]string, error) { return nil, nil, nil }
	fail := true
	sent := 0
	send := func(context.Context, *issuesv1.ReportSessionsRequest) (*issuesv1.ReportSessionsResponse, error) {
		sent++
		if fail {
			return nil, errors.New("HTTP 503")
		}
		return &issuesv1.ReportSessionsResponse{}, nil
	}
	c, _ := newTestCollector(read, send, &clock)
	c.cycle(context.Background())
	fail = false
	c.cycle(context.Background())
	if sent != 2 {
		t.Errorf("sent %d, want 2 (retry after failure)", sent)
	}
}

func TestHTTPSender(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != reportPath || r.Header.Get("Authorization") != "Bearer tka_test" || r.Method != http.MethodPost {
			http.Error(w, `{"code":16}`, http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req issuesv1.ReportSessionsRequest
		if err := protojson.Unmarshal(body, &req); err != nil || req.GetMachine() != "PC-01" {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"created":1,"unchanged":0,"futureField":true}`))
	}))
	defer srv.Close()
	res, err := httpSender(srv.URL+"/", "tka_test", srv.Client())(context.Background(), &issuesv1.ReportSessionsRequest{Machine: "PC-01"})
	if err != nil || res.GetCreated() != 1 {
		t.Fatalf("httpSender: %+v, %v", res, err)
	}
	_, err = httpSender(srv.URL, "tka_bad", srv.Client())(context.Background(), &issuesv1.ReportSessionsRequest{Machine: "PC-01"})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "tka_bad") {
		t.Errorf("401: %v", err)
	}
}
