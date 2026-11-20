package main

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// Lo que se lee de herdr (v0.9.5): `herdr agent list` y `herdr workspace
// list`, con salida JSON {"id": ..., "result": {...}}. Solo los campos que
// se usan; el resto (state_labels, terminal_title...) se ignora.

type herdrAgent struct {
	Agent        string `json:"agent"`
	Name         string `json:"name"`
	AgentStatus  string `json:"agent_status"`
	Cwd          string `json:"cwd"`
	WorkspaceID  string `json:"workspace_id"`
	TerminalID   string `json:"terminal_id"`
	AgentSession struct {
		Value string `json:"value"`
	} `json:"agent_session"`
	Tokens map[string]string `json:"tokens"`
}

type herdrWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

func parseAgents(b []byte) ([]herdrAgent, error) {
	var out struct {
		Result struct {
			Agents []herdrAgent `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("herdr agent list: %w", err)
	}
	return out.Result.Agents, nil
}

func parseWorkspaces(b []byte) (map[string]string, error) {
	var out struct {
		Result struct {
			Workspaces []herdrWorkspace `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("herdr workspace list: %w", err)
	}
	labels := make(map[string]string, len(out.Result.Workspaces))
	for _, w := range out.Result.Workspaces {
		labels[w.WorkspaceID] = w.Label
	}
	return labels, nil
}

// config es lo que necesita la traducción (sin red ni token).
type config struct {
	// root: raíz de los repos (C:\dev). cwd_rel es relativo a ella.
	root string
	// projects: nombre en minúsculas → clave en Tekitla (round → RND).
	projects map[string]string
}

// translate arma la lista completa de la máquina. Las sesiones sin id
// estable se saltean (no se podrían reconocer en el siguiente reporte).
func translate(cfg config, agents []herdrAgent, workspaces map[string]string) []*issuesv1.ReportedSession {
	out := make([]*issuesv1.ReportedSession, 0, len(agents))
	seen := map[string]bool{}
	for _, a := range agents {
		ext := a.AgentSession.Value
		// Un id de más de 200 caracteres haría rechazar el reporte entero: esa
		// sesión se saltea.
		if ext == "" || utf8.RuneCountInString(ext) > 200 {
			continue
		}
		if seen[ext] {
			continue
		}
		seen[ext] = true
		s := &issuesv1.ReportedSession{
			ExternalId:     ext,
			Tool:           toolOf(a.Agent),
			CwdRel:         relCwd(cfg.root, a.Cwd),
			SessionName:    clip(a.Name, 200),
			WorkspaceLabel: clip(workspaces[a.WorkspaceID], 200),
			Status:         statusOf(a.AgentStatus),
			WaitingText:    clip(a.Tokens["espera"], 200),
		}
		s.WaitingOn, s.WaitingTarget = waitingOf(a.Tokens)
		s.WaitingTarget = clip(s.WaitingTarget, 80)
		// Con espera, o en esperando-ok sin token (pide permiso o respuesta).
		if s.WaitingOn != issuesv1.WaitingOn_WAITING_ON_NONE || s.Status == issuesv1.SessionStatus_SESSION_STATUS_WAITING_OK {
			s.WaitingReason = reasonOf(a.Tokens["espera_motivo"])
		}
		// Un panel que trabaja no espera (CU-026-12): el token puede ser de
		// un turno anterior. El servicio aplica la misma regla.
		if s.Status == issuesv1.SessionStatus_SESSION_STATUS_WORKING || s.Status == issuesv1.SessionStatus_SESSION_STATUS_ENDED {
			s.WaitingOn, s.WaitingTarget, s.WaitingText = issuesv1.WaitingOn_WAITING_ON_NONE, "", ""
			s.WaitingReason = issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED
		}
		s.ProjectKey, s.IssueNumber, s.Link = link(cfg, a.Name, s.WorkspaceLabel, s.CwdRel)
		out = append(out, s)
	}
	return out
}

// clip recorta s a max runas (los límites de ReportedSession): un panel con
// un texto largo no puede hacer rechazar el reporte de toda la máquina, que
// además es el latido.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

func toolOf(agent string) issuesv1.AgentTool {
	switch strings.ToLower(agent) {
	case "claude":
		return issuesv1.AgentTool_AGENT_TOOL_CLAUDE_CODE
	case "codex":
		return issuesv1.AgentTool_AGENT_TOOL_CODEX
	}
	return issuesv1.AgentTool_AGENT_TOOL_OTHER
}

// statusOf traduce el estado de herdr (issue 026, §1).
func statusOf(s string) issuesv1.SessionStatus {
	switch s {
	case "working":
		return issuesv1.SessionStatus_SESSION_STATUS_WORKING
	case "blocked":
		return issuesv1.SessionStatus_SESSION_STATUS_WAITING_OK
	case "idle", "done":
		// En herdr `done` es «terminó su turno» con el panel abierto: en
		// reposo (decisión de Victor, 2026-11-18, plan del 026). «Terminada»
		// es solo para los paneles que desaparecen del reporte (lo hace el
		// servicio).
		return issuesv1.SessionStatus_SESSION_STATUS_IDLE
	}
	return issuesv1.SessionStatus_SESSION_STATUS_UNKNOWN
}

// reasonOf traduce el token espera_motivo de espera.ps1 -Motivo (issue 036).
// Un valor desconocido va sin motivo: el script lo rechaza, pero un panel
// viejo o escrito a mano no puede hacer fallar el reporte.
func reasonOf(motivo string) issuesv1.WaitingReason {
	switch strings.ToLower(strings.TrimSpace(motivo)) {
	case "revision":
		return issuesv1.WaitingReason_WAITING_REASON_REVIEW
	case "pruebas":
		return issuesv1.WaitingReason_WAITING_REASON_TESTING
	case "decision":
		return issuesv1.WaitingReason_WAITING_REASON_DECISION
	case "deploy":
		return issuesv1.WaitingReason_WAITING_REASON_DEPLOY
	}
	return issuesv1.WaitingReason_WAITING_REASON_UNSPECIFIED
}

var esperaAPrefix = regexp.MustCompile(`^⏳\s*espera a\s+(.+)$`)

// waitingOf lee los tokens de espera.ps1: espera_tipo y espera_a mandan; el
// texto (espera) solo se mira si falta espera_tipo (paneles de una versión
// vieja del script).
func waitingOf(tokens map[string]string) (issuesv1.WaitingOn, string) {
	switch tokens["espera_tipo"] {
	case "persona":
		return issuesv1.WaitingOn_WAITING_ON_PERSON, ""
	case "sesion":
		return issuesv1.WaitingOn_WAITING_ON_SESSION, tokens["espera_a"]
	case "otro":
		return issuesv1.WaitingOn_WAITING_ON_OTHER, tokens["espera_a"]
	case "":
	default:
		return issuesv1.WaitingOn_WAITING_ON_OTHER, tokens["espera_a"]
	}
	text := strings.TrimSpace(tokens["espera"])
	switch {
	case text == "":
		return issuesv1.WaitingOn_WAITING_ON_NONE, ""
	case strings.HasPrefix(text, "🙋"):
		return issuesv1.WaitingOn_WAITING_ON_PERSON, ""
	}
	if m := esperaAPrefix.FindStringSubmatch(text); m != nil {
		return issuesv1.WaitingOn_WAITING_ON_SESSION, strings.TrimSpace(m[1])
	}
	return issuesv1.WaitingOn_WAITING_ON_OTHER, ""
}

// relCwd devuelve cwd relativo a root con "/": "." en la raíz y "" fuera de
// ella. Sin distinguir mayúsculas (Windows).
func relCwd(root, cwd string) string {
	if root == "" || cwd == "" {
		return ""
	}
	// Las barras se normalizan a mano (no con filepath): herdr da rutas de
	// Windows y el recolector se compila y se prueba también en Linux.
	r := path.Clean(strings.ReplaceAll(root, `\`, "/"))
	c := path.Clean(strings.ReplaceAll(cwd, `\`, "/"))
	r = strings.TrimSuffix(r, "/")
	if strings.EqualFold(c, r) {
		return "."
	}
	if len(c) <= len(r) || !strings.EqualFold(c[:len(r)], r) || c[len(r)] != '/' {
		return ""
	}
	rel := c[len(r)+1:]
	if utf8.RuneCountInString(rel) > 500 {
		return ""
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return ""
		}
	}
	return rel
}

var (
	nameRe      = regexp.MustCompile(`^(.+)-issue-([1-9][0-9]{0,8})$`)
	workspaceRe = regexp.MustCompile(`^(.+?)\s*#([1-9][0-9]{0,8})$`)
	issueDirRe  = regexp.MustCompile(`^issue-([1-9][0-9]{0,8})$`)
)

// link aplica las reglas del §2 en orden: nombre del agente, workspace y
// cwd. Una regla reconoce la sesión solo si el proyecto está en el mapa.
func link(cfg config, name, workspace, cwdRel string) (string, int32, issuesv1.SessionLink) {
	if m := nameRe.FindStringSubmatch(strings.TrimSpace(name)); m != nil {
		if key, n, ok := lookup(cfg, m[1], m[2]); ok {
			return key, n, issuesv1.SessionLink_SESSION_LINK_AGENT_NAME
		}
	}
	if m := workspaceRe.FindStringSubmatch(strings.TrimSpace(workspace)); m != nil {
		if key, n, ok := lookup(cfg, m[1], m[2]); ok {
			return key, n, issuesv1.SessionLink_SESSION_LINK_WORKSPACE
		}
	}
	if key, n, ok := linkCwd(cfg, cwdRel); ok {
		return key, n, issuesv1.SessionLink_SESSION_LINK_CWD
	}
	return "", 0, issuesv1.SessionLink_SESSION_LINK_NONE
}

// linkCwd reconoce <proyecto>/.../worktrees/issue-<n>/... y
// <repo>-wt/issue-<n>/...; el proyecto es la carpeta raíz.
func linkCwd(cfg config, cwdRel string) (string, int32, bool) {
	segs := strings.Split(cwdRel, "/")
	if len(segs) < 2 || segs[0] == "." || segs[0] == "" {
		return "", 0, false
	}
	project := segs[0]
	for i := 1; i < len(segs); i++ {
		m := issueDirRe.FindStringSubmatch(segs[i])
		if m == nil {
			continue
		}
		parentIsWorktrees := segs[i-1] == "worktrees"
		wtRoot := i == 1 && strings.HasSuffix(project, "-wt")
		if parentIsWorktrees || wtRoot {
			return lookup(cfg, project, m[1])
		}
	}
	return "", 0, false
}

func lookup(cfg config, project, number string) (string, int32, bool) {
	key, ok := cfg.projects[strings.ToLower(strings.TrimSpace(project))]
	if !ok {
		return "", 0, false
	}
	n, err := strconv.ParseInt(number, 10, 32)
	if err != nil {
		return "", 0, false
	}
	return key, int32(n), true
}

// parseProjects lee "round=RND,tekitla=TK" (nombres sin mayúsculas).
func parseProjects(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, key, ok := strings.Cut(pair, "=")
		name, key = strings.ToLower(strings.TrimSpace(name)), strings.ToUpper(strings.TrimSpace(key))
		if !ok || name == "" || key == "" {
			return nil, fmt.Errorf("proyecto mal escrito: %q (se espera nombre=CLAVE)", pair)
		}
		out[name] = key
	}
	return out, nil
}
