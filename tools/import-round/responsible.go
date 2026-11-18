package main

// Responsable de un issue de Round (issue 039, CU-039-6 y CU-039-9).
//
//   - Coincide con un agente no revocado de quien importa (por nombre, sin
//     mayúsculas ni acentos) o con -yo (quien importa): se asigna.
//   - No coincide: etiqueta responsable/<nombre> (decisión de Victor,
//     2026-11-18), sin asignar, y sale en el reporte.
//
// Las personas no tienen nombre en Tekitla: por eso -yo.

import (
	"context"
	"fmt"

	issuesv1 "github.com/tekitla/tekitla-protos/gen/go/issues/v1"
)

// ResponsiblePrefix es el prefijo de la etiqueta de un responsable sin cuenta.
const ResponsiblePrefix = "responsable"

// responsibles son los actores con los que se compara un «Responsable:».
type responsibles struct {
	// byName: nameKey del nombre -> actor (solo agentes no revocados).
	byName map[string]actorRef
	// me es el nameKey de -yo y meID el actor de quien importa.
	me, meID string
}

type actorRef struct{ id, name string }

// anyResponsible: algún doc tiene «Responsable:» (si no, no hace falta
// pedir los actores).
func anyResponsible(docs []*Doc) bool {
	for _, d := range docs {
		if ResponsibleName(d.RawResponsible) != "" {
			return true
		}
	}
	return false
}

// loadResponsibles trae los actores de quien importa: su persona (para -yo)
// y sus agentes no revocados.
func loadResponsibles(ctx context.Context, client issuesv1.ActorServiceClient, uid, me string) (responsibles, error) {
	if client == nil {
		return responsibles{}, fmt.Errorf("sin cliente de ActorService")
	}
	res, err := client.ListActors(ctx, &issuesv1.ListActorsRequest{})
	if err != nil {
		return responsibles{}, fmt.Errorf("ListActors: %w", err)
	}
	r := responsibles{byName: map[string]actorRef{}, me: nameKey(me)}
	for _, a := range res.GetActors() {
		if a.GetRevokedAt() != nil {
			continue
		}
		switch a.GetKind() {
		case issuesv1.ActorKind_ACTOR_KIND_PERSON:
			if a.GetUserId() == uid {
				r.meID = a.GetId()
			}
		case issuesv1.ActorKind_ACTOR_KIND_AGENT:
			if k := nameKey(a.GetDisplayName()); k != "" {
				if _, dup := r.byName[k]; !dup {
					r.byName[k] = actorRef{id: a.GetId(), name: a.GetDisplayName()}
				}
			}
		}
	}
	return r, nil
}

// resolve completa el responsable de t: el actor si coincide o, si no, la
// etiqueta responsable/<nombre>.
func (r responsibles) resolve(d *Doc, t translation) translation {
	name := ResponsibleName(d.RawResponsible)
	if name == "" {
		return t
	}
	t.Responsible = name
	key := nameKey(name)
	if a, ok := r.byName[key]; ok {
		t.AssigneeID, t.AssigneeName = a.id, a.name
		return t
	}
	if r.me != "" && key == r.me && r.meID != "" {
		t.AssigneeID, t.AssigneeName = r.meID, name
		return t
	}
	var dropped bool
	t.Labels, dropped = addLabels(t.Labels, CategoryLabels(ResponsiblePrefix, name)...)
	t.LabelsDropped = t.LabelsDropped || dropped
	return t
}
